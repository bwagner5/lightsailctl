package instance

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/lightsail"
	"github.com/aws/lightsailctl/pkg/names"
)

func createOp(s *Store) registry.Operation {
	return registry.Operation{
		Name: "create", Key: "c", Short: "create a new Lightsail instance",
		Confirm: "Create this instance?",
		Fields:  CreateFields(s),
		Steps:   []registry.Step{CreateStep(s)},
	}
}

// CreateFields returns the Fields the instance-create wizard prompts for.
// The returned closures share state (blueprint-type filters blueprint;
// blueprint filters bundle) and must be collected in order. Call this
// exactly once per saga — the shared pointers are reallocated on each
// call, so two concurrent callers would get tangled filtering state.
//
// The store is the read-side the pickers hit. Callers that want the
// pickers to honor a region already pinned by a parent flow should pass
// the same Store they're using elsewhere (see NewStore).
func CreateFields(s *Store) []registry.Field {
	bpType := "os"           // shared: blueprint-type → blueprint filtering
	platform := "LINUX_UNIX" // shared: blueprint → bundle filtering
	bundleCat := lightsail.BundleCategoryGeneralPurpose
	ipType := "dualstack"
	bpSuggest, bpValidate := BlueprintSuggestAndValidate(s, &bpType, &platform)
	return []registry.Field{
		{Flag: "name", Short: "n", Help: "instance name", Required: true,
			Prefill: names.Random, Validate: names.ValidateLabel},
		{Flag: "region", Short: "r", Help: "AWS region", Required: true,
			Default: "us-east-1", Suggest: RegionSuggest(s)},
		{Flag: "blueprint-type", Help: "blueprint category", Default: "os",
			Suggest: func(_ context.Context) ([]registry.Choice, error) {
				return []registry.Choice{
					{Value: "os", Display: "os    Base operating systems (Amazon Linux, Ubuntu, Debian, …)"},
					{Value: "app", Display: "app   Pre-configured applications (WordPress, LAMP, Node.js, …)"},
				}, nil
			},
			Validate: func(v string) error {
				bpType = v // capture selection for blueprint filtering
				return nil
			}},
		{Flag: "blueprint", Short: "b", Help: "OS / image", Required: true,
			Default: "amazon_linux_2023", Suggest: bpSuggest, Validate: bpValidate},
		{Flag: "ip-address-type", Help: "networking stack", Default: "dualstack",
			Suggest: func(_ context.Context) ([]registry.Choice, error) {
				return []registry.Choice{
					{Value: "dualstack", Display: "dualstack  IPv4 + IPv6"},
					{Value: "ipv6", Display: "ipv6       IPv6 only"},
				}, nil
			},
			Validate: func(v string) error {
				ipType = v
				return nil
			}},
		{Flag: "bundle-category", Help: "instance category",
			Default: lightsail.BundleCategoryGeneralPurpose,
			Suggest: BundleCategorySuggest(),
			Validate: func(v string) error {
				bundleCat = v
				return nil
			}},
		{Flag: "bundle", Help: "instance size", Required: true,
			Default: "micro_x_x", Suggest: BundleSuggestFiltered(s, &platform, &bundleCat, &ipType)},
		{Flag: "user-data", Help: "launch script", File: true},
		{Flag: "monitoring", Help: "detailed monitoring", Default: "false", Kind: registry.KindBool,
			Suggest: func(_ context.Context) ([]registry.Choice, error) {
				return []registry.Choice{
					{Value: "false", Display: "No   Basic monitoring (free)"},
					{Value: "true", Display: "Yes  Detailed monitoring (additional cost)"},
				}, nil
			}},
	}
}

// CreateStep returns the single step that runs CreateInstance with the
// inputs produced by CreateFields. Exposed so the deploy flow can splice
// instance creation into its own saga without duplicating the body.
func CreateStep(s *Store) registry.Step {
	return registry.Step{Label: "Create instance", Do: createInstanceStep(s)}
}

// regionSuggest builds the region picker's Suggest callback. Fetches
// the live region list via Client.Regions (disk-cached; snapshot
// fallback if offline), sorts and column-aligns it into triad.Choices
// matching the old output layout.
func RegionSuggest(s *Store) func(context.Context) ([]registry.Choice, error) {
	return func(ctx context.Context) ([]registry.Choice, error) {
		c, err := s.ensure(ctx)
		if err != nil {
			return nil, err
		}
		regs, err := c.Regions(ctx)
		if err != nil {
			return nil, err
		}
		maxID, maxGroup := 0, 0
		for _, r := range regs {
			if len(r.ID) > maxID {
				maxID = len(r.ID)
			}
			if g := c.RegionGroup(ctx, r.ID); len(g) > maxGroup {
				maxGroup = len(g)
			}
		}
		out := make([]registry.Choice, 0, len(regs))
		for _, r := range regs {
			out = append(out, registry.Choice{
				Value:   r.ID,
				Display: fmt.Sprintf("%-*s  %-*s  %s", maxGroup, c.RegionGroup(ctx, r.ID), maxID, r.ID, r.DisplayName),
			})
		}
		return out, nil
	}
}

func BlueprintSuggestAndValidate(s *Store, bpType, platform *string) (func(context.Context) ([]registry.Choice, error), func(string) error) {
	var cached []lightsail.Blueprint
	suggest := func(ctx context.Context) ([]registry.Choice, error) {
		c, err := s.ensure(ctx)
		if err != nil {
			return nil, err
		}
		rc := c
		if c.Region() == "" {
			rc = c.WithRegion(c.DefaultRegion())
		}
		bps, err := rc.ListBlueprints(ctx)
		if err != nil {
			return nil, err
		}
		cached = bps
		filter := *bpType
		sort.SliceStable(bps, func(i, j int) bool { return bps[i].Name < bps[j].Name })
		out := make([]registry.Choice, 0, len(bps))
		for _, b := range bps {
			if filter != "" && b.Type != filter {
				continue
			}
			out = append(out, registry.Choice{
				Value:   b.ID,
				Display: fmt.Sprintf("%-30s  %s", b.Name, b.Platform),
			})
		}
		return out, nil
	}
	validate := func(v string) error {
		for _, b := range cached {
			if b.ID == v {
				*platform = b.Platform
				return nil
			}
		}
		return nil
	}
	return suggest, validate
}

func BundleSuggest(s *Store, platform *string) func(context.Context) ([]registry.Choice, error) {
	return BundleSuggestFiltered(s, platform, nil)
}

func BundleSuggestFiltered(s *Store, platform *string, category *string, ipType ...*string) func(context.Context) ([]registry.Choice, error) {
	return func(ctx context.Context) ([]registry.Choice, error) {
		c, err := s.ensure(ctx)
		if err != nil {
			return nil, err
		}
		rc := c
		if c.Region() == "" {
			rc = c.WithRegion(c.DefaultRegion())
		}
		bundles, err := rc.ListBundles(ctx)
		if err != nil {
			return nil, err
		}
		plat := *platform
		cat := ""
		if category != nil {
			cat = *category
		}
		ip := ""
		if len(ipType) > 0 && ipType[0] != nil {
			ip = *ipType[0]
		}

		// When duplicate bundles exist at different price points for
		// the same specs (vCPU+RAM+Disk), the cheaper one is the
		// IPv6-only variant. Build a set of the cheaper IDs so we can
		// filter by networking choice.
		type specKey struct {
			VCPUs int32
			RAM   float32
			Disk  int32
			Cat   string
		}
		cheaperIDs := map[string]bool{}
		if ip != "" {
			best := map[specKey]lightsail.Bundle{}
			worst := map[specKey]lightsail.Bundle{}
			for _, b := range bundles {
				if plat != "" && !containsPlatform(b.Platforms, plat) {
					continue
				}
				k := specKey{b.VCPUs, b.RAM, b.Disk, b.Category()}
				if prev, ok := best[k]; !ok || b.Price < prev.Price {
					if ok {
						worst[k] = prev
					}
					best[k] = b
				} else if _, ok := worst[k]; !ok || b.Price > worst[k].Price {
					worst[k] = b
				}
			}
			for k, b := range best {
				if _, hasDup := worst[k]; hasDup {
					cheaperIDs[b.ID] = true
				}
			}
		}

		out := make([]registry.Choice, 0, len(bundles))
		for _, b := range bundles {
			if plat != "" && !containsPlatform(b.Platforms, plat) {
				continue
			}
			if cat != "" && b.Category() != cat {
				continue
			}
			// Filter by IP type: ipv6 shows only the cheaper tier,
			// dualstack shows only the more expensive tier.
			if ip == "ipv6" && len(cheaperIDs) > 0 && !cheaperIDs[b.ID] {
				continue
			}
			if ip == "dualstack" && cheaperIDs[b.ID] {
				continue
			}
			out = append(out, registry.Choice{
				Value:   b.ID,
				Display: fmt.Sprintf("%-12s  %d vCPU  %4.1f GB RAM  %3d GB disk  $%6.2f/mo", b.Name, b.VCPUs, b.RAM, b.Disk, b.Price),
			})
		}
		return out, nil
	}
}

// BundleCategorySuggest returns the Suggest function for bundle category selection.
func BundleCategorySuggest() func(context.Context) ([]registry.Choice, error) {
	return func(_ context.Context) ([]registry.Choice, error) {
		return []registry.Choice{
			{Value: lightsail.BundleCategoryGeneralPurpose, Display: "General Purpose       Best for most workloads (balanced vCPU/RAM)"},
			{Value: lightsail.BundleCategoryComputeOptimized, Display: "Compute-optimized     High-performance processors (1:2 vCPU:RAM)"},
			{Value: lightsail.BundleCategoryMemoryOptimized, Display: "Memory-optimized      Memory-intensive workloads (1:8 vCPU:RAM)"},
		}, nil
	}
}

func containsPlatform(platforms []string, target string) bool {
	for _, p := range platforms {
		if p == target {
			return true
		}
	}
	return false
}

func createInstanceStep(s *Store) func(context.Context, *registry.State) error {
	return func(ctx context.Context, st *registry.State) error {
		c, err := s.ensure(ctx)
		if err != nil {
			return err
		}
		region := st.Input.Get("region")
		rc := c.WithRegion(region)
		azs, err := rc.ListAvailabilityZones(ctx)
		if err != nil {
			return fmt.Errorf("list AZs: %w", err)
		}
		az := region + "a"
		if len(azs) > 0 {
			az = azs[0]
		}
		var userData string
		if p := st.Input.Get("user-data"); p != "" {
			b, err := os.ReadFile(p)
			if err != nil {
				return fmt.Errorf("read user-data %s: %w", p, err)
			}
			userData = string(b)
		}
		monitoring, err := st.Input.Bool("monitoring")
		if err != nil {
			return err
		}
		return rc.CreateInstance(ctx,
			st.Input.Get("name"),
			az,
			st.Input.Get("blueprint"),
			st.Input.Get("bundle"),
			st.Input.Get("ip-address-type"),
			userData,
			monitoring,
		)
	}
}
