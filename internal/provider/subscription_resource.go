package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	mathrand "math/rand"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/streaming"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/managementgroups/armmanagementgroups"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/subscription/armsubscription"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/blockblob"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

var (
	_ resource.Resource                = &subscriptionResource{}
	_ resource.ResourceWithConfigure   = &subscriptionResource{}
	_ resource.ResourceWithImportState = &subscriptionResource{}
)

// NewSubscriptionResource is registered in provider.go's Resources.
func NewSubscriptionResource() resource.Resource { return &subscriptionResource{} }

type subscriptionResource struct{ c *clients }

type subscriptionModel struct {
	DisplayName    types.String `tfsdk:"display_name"`
	SubscriptionID types.String `tfsdk:"subscription_id"`
	ID             types.String `tfsdk:"id"`
	FromPool       types.Bool   `tfsdk:"from_pool"`
}

func (r *subscriptionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_subscription"
}

func (r *subscriptionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An Azure subscription, claimed from the pool management group or created when the pool is empty. Cancelled on destroy.",
		Attributes: map[string]schema.Attribute{
			"display_name": schema.StringAttribute{
				Required:    true,
				Description: "Subscription name. A claimed subscription is renamed to this.",
			},
			"subscription_id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"from_pool": schema.BoolAttribute{
				Computed:    true,
				Description: "True if this subscription came from the pool rather than being created.",
			},
		},
	}
}

func (r *subscriptionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	r.c = req.ProviderData.(*clients)
}

func (r *subscriptionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan subscriptionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.DisplayName.ValueString()

	id, err := r.c.claim(ctx, name)
	fromPool := err == nil && id != ""
	if err != nil {
		resp.Diagnostics.AddError("Claiming subscription from pool", err.Error())
		return
	}
	if id == "" {
		tflog.Info(ctx, "subscription pool empty, creating a new subscription")
		if r.c.cfg.BillingScope.ValueString() == "" {
			resp.Diagnostics.AddError("Subscription pool empty",
				fmt.Sprintf("No subscriptions available in %s and no billing_scope configured to create one.", r.c.cfg.PoolManagementGroupID.ValueString()))
			return
		}
		id, err = r.c.create(ctx, name)
		if err != nil {
			resp.Diagnostics.AddError("Creating subscription", err.Error())
			return
		}
	}

	plan.SubscriptionID = types.StringValue(id)
	plan.ID = types.StringValue(id)
	plan.FromPool = types.BoolValue(fromPool)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *subscriptionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state subscriptionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	got, err := r.c.subs.Get(ctx, state.SubscriptionID.ValueString(), nil)
	if isNotFound(err) || (err == nil && !usable(got.State)) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading subscription", err.Error())
		return
	}
	if got.DisplayName != nil {
		state.DisplayName = types.StringValue(*got.DisplayName)
	}
	state.ID = state.SubscriptionID
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *subscriptionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state subscriptionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := plan.DisplayName.ValueString()
	if name != state.DisplayName.ValueString() {
		if _, err := r.c.sub.Rename(ctx, state.SubscriptionID.ValueString(), armsubscription.Name{SubscriptionName: &name}, nil); err != nil {
			resp.Diagnostics.AddError("Renaming subscription", err.Error())
			return
		}
	}
	plan.SubscriptionID = state.SubscriptionID
	plan.ID = state.ID
	plan.FromPool = state.FromPool
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *subscriptionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state subscriptionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.c.sub.Cancel(ctx, state.SubscriptionID.ValueString(), nil); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Cancelling subscription", err.Error())
	}
}

func (r *subscriptionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("subscription_id"), req, resp)
}

// ------------------------------------------------- azure calls behind the CRUD

// claim takes one subscription out of the pool and moves it to the claimed
// management group. Returns "" when the pool is empty.
//
// The lock is a blob per subscription in the claim container, created with
// If-None-Match: * — Azure Storage lets exactly one writer win, so parallel
// applies can never claim the same subscription. The management group move
// alone is not enough: the MG hierarchy API is eventually consistent, so a
// concurrent run can still see a moved subscription sitting in the pool.
func (c *clients) claim(ctx context.Context, displayName string) (string, error) {
	pager := c.mgSubs.NewGetSubscriptionsUnderManagementGroupPager(c.cfg.PoolManagementGroupID.ValueString(), nil)
	var ids []string
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return "", err
		}
		ids = append(ids, poolCandidates(page.Value)...)
	}
	// Shuffle so parallel runs mostly reach for different subscriptions and
	// rarely have to fall through to a second candidate.
	mathrand.Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })

	id, err := claimFirst(ids, func(id string) (bool, error) {
		claimed, err := c.lockClaim(ctx, id, displayName)
		if err != nil || !claimed {
			return false, err
		}
		if _, err := c.mgSubs.Create(ctx, c.cfg.ClaimedManagementGroupID.ValueString(), id, nil); err != nil {
			// Give the subscription back, or it is lost to the pool forever.
			if _, delErr := c.claims.NewBlockBlobClient(id).Delete(ctx, nil); delErr != nil {
				return false, fmt.Errorf("moving %s to %s failed (%v) and releasing the claim failed too: %w",
					id, c.cfg.ClaimedManagementGroupID.ValueString(), err, delErr)
			}
			tflog.Warn(ctx, "could not move claimed subscription, released it and trying next",
				map[string]any{"subscription_id": id, "error": err.Error()})
			return false, nil
		}
		return true, nil
	})
	if err != nil || id == "" {
		return "", err
	}
	if _, err := c.sub.Rename(ctx, id, armsubscription.Name{SubscriptionName: &displayName}, nil); err != nil {
		return id, fmt.Errorf("claimed %s but renaming failed: %w", id, err)
	}
	return id, nil
}

// claimFirst returns the first id that take() wins. "" means all candidates
// went to someone else (or there were none).
func claimFirst(ids []string, take func(string) (bool, error)) (string, error) {
	for _, id := range ids {
		ok, err := take(id)
		if err != nil {
			return "", err
		}
		if ok {
			return id, nil
		}
	}
	return "", nil
}

// lockClaim creates the claim blob for a subscription. false means another
// apply got there first.
func (c *clients) lockClaim(ctx context.Context, id, displayName string) (bool, error) {
	body := fmt.Sprintf("%s claimed for %q at %s\n", id, displayName, time.Now().UTC().Format(time.RFC3339))
	_, err := c.claims.NewBlockBlobClient(id).Upload(ctx, streaming.NopCloser(strings.NewReader(body)), &blockblob.UploadOptions{
		AccessConditions: &blob.AccessConditions{
			ModifiedAccessConditions: &blob.ModifiedAccessConditions{IfNoneMatch: to.Ptr(azcore.ETagAny)},
		},
	})
	if bloberror.HasCode(err, bloberror.BlobAlreadyExists, bloberror.ConditionNotMet) {
		return false, nil
	}
	if bloberror.HasCode(err, bloberror.ContainerNotFound) {
		// First ever claim: make the container and try once more. Doing this
		// lazily keeps the normal path to a single call, and works when the
		// role assignment is scoped to the container itself.
		if _, err := c.claims.Create(ctx, nil); err != nil && !bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
			return false, fmt.Errorf("creating claim container: %w", err)
		}
		return c.lockClaim(ctx, id, displayName)
	}
	return err == nil, err
}

// create provisions a new subscription straight into the claimed management group.
func (c *clients) create(ctx context.Context, displayName string) (string, error) {
	workload := armsubscription.WorkloadProduction
	if w := c.cfg.Workload.ValueString(); w != "" {
		workload = armsubscription.Workload(w)
	}
	aliasName := "tf-" + randHex()
	claimed := c.cfg.ClaimedManagementGroupID.ValueString()
	billing := c.cfg.BillingScope.ValueString()

	poller, err := c.alias.BeginCreate(ctx, aliasName, armsubscription.PutAliasRequest{
		Properties: &armsubscription.PutAliasRequestProperties{
			DisplayName:  &displayName,
			BillingScope: &billing,
			Workload:     &workload,
			AdditionalProperties: &armsubscription.PutAliasRequestAdditionalProperties{
				ManagementGroupID: &claimed,
			},
		},
	}, nil)
	if err != nil {
		return "", err
	}
	res, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		return "", err
	}
	if res.Properties == nil || res.Properties.SubscriptionID == nil {
		return "", errors.New("subscription created but no subscription id returned")
	}
	// The alias is just a creation receipt; dropping it leaves the subscription alone.
	if _, err := c.alias.Delete(ctx, aliasName, nil); err != nil {
		tflog.Warn(ctx, "could not delete subscription alias", map[string]any{"alias": aliasName, "error": err.Error()})
	}
	return *res.Properties.SubscriptionID, nil
}

func randHex() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck // crypto/rand.Read never fails per docs
	return hex.EncodeToString(b)
}

// poolCandidates returns the ids of subscriptions that are actually claimable.
func poolCandidates(subs []*armmanagementgroups.SubscriptionUnderManagementGroup) []string {
	var ids []string
	for _, s := range subs {
		if s == nil || s.Name == nil || *s.Name == "" {
			continue
		}
		if s.Properties != nil && s.Properties.State != nil && *s.Properties.State != "Enabled" {
			continue
		}
		ids = append(ids, *s.Name)
	}
	return ids
}

func usable(state *armsubscription.SubscriptionState) bool {
	return state == nil || (*state != armsubscription.SubscriptionStateDeleted && *state != armsubscription.SubscriptionStateDisabled)
}
