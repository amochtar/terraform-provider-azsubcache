// Package provider implements the azsubcache Terraform provider.
package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/managementgroups/armmanagementgroups"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/subscription/armsubscription"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/container"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	providerschema "github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ provider.Provider = &azProvider{}

// New returns the provider constructor the plugin server wants.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &azProvider{version: version} }
}

type azProvider struct{ version string }

type providerModel struct {
	PoolManagementGroupID    types.String `tfsdk:"pool_management_group_id"`
	ClaimedManagementGroupID types.String `tfsdk:"claimed_management_group_id"`
	ClaimContainerURL        types.String `tfsdk:"claim_container_url"`
	BillingScope             types.String `tfsdk:"billing_scope"`
	Workload                 types.String `tfsdk:"workload"`
}

// clients is what resources get handed in Configure.
type clients struct {
	subs   *armsubscription.SubscriptionsClient
	sub    *armsubscription.Client
	alias  *armsubscription.AliasClient
	mgSubs *armmanagementgroups.ManagementGroupSubscriptionsClient
	claims *container.Client
	cfg    providerModel
}

func (p *azProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "azsubcache"
	resp.Version = p.version
}

func (p *azProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = providerschema.Schema{
		MarkdownDescription: "Hands out Azure subscriptions from a pool of pre-provisioned ones.\n\n" +
			"`azsubcache_subscription` claims a subscription sitting in the pool management group and moves it to " +
			"the claimed management group; only when the pool is empty does it create one, which is the slow, " +
			"rate-limited path. Claims are arbitrated by a blob per subscription in `claim_container_url`, so " +
			"parallel applies never hand out the same subscription twice.\n\n" +
			"Authentication uses `DefaultAzureCredential`: environment variables, workload identity, managed " +
			"identity or `az login`.",
		Attributes: map[string]providerschema.Attribute{
			"pool_management_group_id": providerschema.StringAttribute{
				Optional:    true,
				Description: "Management group holding pre-provisioned, unclaimed subscriptions. Defaults to $AZSUBCACHE_POOL_MANAGEMENT_GROUP_ID.",
			},
			"claimed_management_group_id": providerschema.StringAttribute{
				Optional:    true,
				Description: "Management group a subscription is moved to when claimed. New subscriptions are created directly here. Defaults to $AZSUBCACHE_CLAIMED_MANAGEMENT_GROUP_ID.",
			},
			"claim_container_url": providerschema.StringAttribute{
				Optional:    true,
				Description: "Blob container used to lock claims, e.g. https://acct.blob.core.windows.net/subscription-claims. One blob per claimed subscription; its creation is the claim. Defaults to $AZSUBCACHE_CLAIM_CONTAINER_URL.",
			},
			"billing_scope": providerschema.StringAttribute{
				Optional:    true,
				Description: "Billing scope used when the pool is empty and a subscription must be created, e.g. /billingAccounts/{a}/enrollmentAccounts/{e}. Without it, an empty pool is an error. Defaults to $AZSUBCACHE_BILLING_SCOPE.",
			},
			"workload": providerschema.StringAttribute{
				Optional:    true,
				Description: "Workload type for created subscriptions: Production (default) or DevTest. Defaults to $AZSUBCACHE_WORKLOAD, then Production.",
			},
		},
	}
}

func (p *azProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	cfg.PoolManagementGroupID = envDefault(cfg.PoolManagementGroupID, "AZSUBCACHE_POOL_MANAGEMENT_GROUP_ID")
	cfg.ClaimedManagementGroupID = envDefault(cfg.ClaimedManagementGroupID, "AZSUBCACHE_CLAIMED_MANAGEMENT_GROUP_ID")
	cfg.ClaimContainerURL = envDefault(cfg.ClaimContainerURL, "AZSUBCACHE_CLAIM_CONTAINER_URL")
	cfg.BillingScope = envDefault(cfg.BillingScope, "AZSUBCACHE_BILLING_SCOPE")
	cfg.Workload = envDefault(cfg.Workload, "AZSUBCACHE_WORKLOAD")

	for attr, v := range map[string]types.String{
		"pool_management_group_id":    cfg.PoolManagementGroupID,
		"claimed_management_group_id": cfg.ClaimedManagementGroupID,
		"claim_container_url":         cfg.ClaimContainerURL,
	} {
		if v.ValueString() == "" {
			resp.Diagnostics.AddAttributeError(path.Root(attr), "Missing provider configuration",
				fmt.Sprintf("Set %s in the provider block or $AZSUBCACHE_%s.", attr, strings.ToUpper(attr)))
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		resp.Diagnostics.AddError("Azure credentials", err.Error())
		return
	}
	subFactory, err := armsubscription.NewClientFactory(cred, nil)
	if err != nil {
		resp.Diagnostics.AddError("Azure client", err.Error())
		return
	}
	mgFactory, err := armmanagementgroups.NewClientFactory(cred, nil)
	if err != nil {
		resp.Diagnostics.AddError("Azure client", err.Error())
		return
	}

	claims, err := container.NewClient(cfg.ClaimContainerURL.ValueString(), cred, nil)
	if err != nil {
		resp.Diagnostics.AddError("Azure client", err.Error())
		return
	}

	c := &clients{
		subs:   subFactory.NewSubscriptionsClient(),
		sub:    subFactory.NewClient(),
		alias:  subFactory.NewAliasClient(),
		mgSubs: mgFactory.NewManagementGroupSubscriptionsClient(),
		claims: claims,
		cfg:    cfg,
	}
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *azProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewSubscriptionResource,
	}
}

func (p *azProvider) DataSources(context.Context) []func() datasource.DataSource {
	return nil
}

func isNotFound(err error) bool {
	var respErr *azcore.ResponseError
	return errors.As(err, &respErr) && respErr.StatusCode == 404
}

// envDefault falls back to an environment variable, the usual way a Terraform
// provider gets its settings in CI without templating a provider block.
func envDefault(v types.String, env string) types.String {
	if v.ValueString() != "" {
		return v
	}
	return types.StringValue(os.Getenv(env))
}
