// Command oci.get.ca lists OCI Certificates Management certificate authorities (CAs)
// using instance principal authentication.
//
// It must run on an OCI compute instance whose dynamic group has at least:
//
//	allow dynamic-group <dg> to read certificate-authorities in tenancy
//	allow dynamic-group <dg> to inspect compartments in tenancy   # only for -all
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/oracle/oci-go-sdk/v65/certificatesmanagement"
	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/common/auth"
	"github.com/oracle/oci-go-sdk/v65/identity"
)

type options struct {
	compartmentID string
	allCompart    bool
	region        string
	state         string
	name          string
	asJSON        bool
	pageSize      int
	parallel      int
	timeout       time.Duration
	verbose       bool
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "oci.get.ca: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var opt options
	flag.StringVar(&opt.compartmentID, "compartment", "", "compartment OCID to list (default: the instance's tenancy root)")
	flag.BoolVar(&opt.allCompart, "all", false, "walk every accessible compartment in the tenancy subtree")
	flag.StringVar(&opt.region, "region", "", "override region (default: the instance's own region, e.g. us-ashburn-1)")
	flag.StringVar(&opt.state, "state", "", "lifecycle state filter: ACTIVE, CREATING, UPDATING, DELETING, DELETED, SCHEDULING_DELETION, PENDING_DELETION, FAILED")
	flag.StringVar(&opt.name, "name", "", "exact CA name filter")
	flag.BoolVar(&opt.asJSON, "json", false, "emit JSON instead of a table")
	flag.IntVar(&opt.pageSize, "page-size", 100, "items per page (1-1000)")
	flag.IntVar(&opt.parallel, "parallel", 8, "max concurrent compartment queries when -all is set")
	flag.DurationVar(&opt.timeout, "timeout", 2*time.Minute, "overall deadline")
	flag.BoolVar(&opt.verbose, "v", false, "log per-compartment progress and skipped compartments")
	flag.Parse()

	if opt.pageSize < 1 || opt.pageSize > 1000 {
		return errors.New("-page-size must be between 1 and 1000")
	}
	if opt.parallel < 1 {
		opt.parallel = 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), opt.timeout)
	defer cancel()

	// 1. Instance principal auth. The provider talks to the instance metadata
	//    service (169.254.169.254), fetches the instance's leaf cert + key, and
	//    exchanges them for a short-lived RPST that it refreshes on its own.
	provider, err := newInstancePrincipalProvider(opt.region)
	if err != nil {
		return fmt.Errorf("instance principal auth: %w", err)
	}

	tenancyID, err := provider.TenancyOCID()
	if err != nil {
		return fmt.Errorf("resolve tenancy OCID: %w", err)
	}
	region, err := provider.Region()
	if err != nil {
		return fmt.Errorf("resolve region: %w", err)
	}
	if opt.verbose {
		fmt.Fprintf(os.Stderr, "tenancy=%s region=%s\n", tenancyID, region)
	}

	// 2. Certificates Management control-plane client.
	//    (Note: this is the *management* service. Reading certificate bundles
	//    at runtime is the separate `certificates` data-plane service.)
	client, err := certificatesmanagement.NewCertificatesManagementClientWithConfigurationProvider(provider)
	if err != nil {
		return fmt.Errorf("build certificatesmanagement client: %w", err)
	}
	retry := common.DefaultRetryPolicy()
	client.SetRegion(region)

	// 3. Work out which compartments to query. ListCertificateAuthorities has no
	//    compartmentIdInSubtree parameter, so "all CAs in the tenancy" means
	//    enumerating compartments and querying each one.
	compartments := []string{opt.compartmentID}
	switch {
	case opt.allCompart:
		compartments, err = listCompartments(ctx, provider, region, tenancyID, &retry)
		if err != nil {
			return err
		}
		if opt.verbose {
			fmt.Fprintf(os.Stderr, "scanning %d compartments\n", len(compartments))
		}
	case opt.compartmentID == "":
		compartments = []string{tenancyID}
	}

	// 4. Fan out, bounded.
	var (
		mu      sync.Mutex
		all     []certificatesmanagement.CertificateAuthoritySummary
		firstEr error
		wg      sync.WaitGroup
		sem     = make(chan struct{}, opt.parallel)
	)
	for _, cid := range compartments {
		cid := cid
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			cas, err := listCAs(ctx, client, cid, opt, &retry)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				// A compartment we can see but may not read CAs in is normal in
				// a wide scan; don't let it kill the whole run.
				if opt.allCompart && isAuthzOrNotFound(err) {
					if opt.verbose {
						fmt.Fprintf(os.Stderr, "skip %s: %v\n", cid, err)
					}
					return
				}
				if firstEr == nil {
					firstEr = err
				}
				return
			}
			all = append(all, cas...)
		}()
	}
	wg.Wait()
	if firstEr != nil {
		return firstEr
	}

	sort.Slice(all, func(i, j int) bool {
		return strings.ToLower(deref(all[i].Name)) < strings.ToLower(deref(all[j].Name))
	})

	return render(all, opt.asJSON)
}

// newInstancePrincipalProvider builds an instance principal configuration
// provider, optionally pinned to a region other than the instance's own.
func newInstancePrincipalProvider(region string) (common.ConfigurationProvider, error) {
	if region != "" {
		return auth.InstancePrincipalConfigurationProviderForRegion(common.StringToRegion(region))
	}
	return auth.InstancePrincipalConfigurationProvider()
}

// listCAs pages through all CAs in one compartment.
func listCAs(
	ctx context.Context,
	client certificatesmanagement.CertificatesManagementClient,
	compartmentID string,
	opt options,
	retry *common.RetryPolicy,
) ([]certificatesmanagement.CertificateAuthoritySummary, error) {
	var (
		out  []certificatesmanagement.CertificateAuthoritySummary
		page *string
	)
	for {
		req := certificatesmanagement.ListCertificateAuthoritiesRequest{
			CompartmentId:   common.String(compartmentID),
			Limit:           common.Int(opt.pageSize),
			Page:            page,
			SortBy:          certificatesmanagement.ListCertificateAuthoritiesSortByName,
			SortOrder:       certificatesmanagement.ListCertificateAuthoritiesSortOrderAsc,
			RequestMetadata: common.RequestMetadata{RetryPolicy: retry},
		}
		if opt.name != "" {
			req.Name = common.String(opt.name)
		}
		if opt.state != "" {
			req.LifecycleState = certificatesmanagement.ListCertificateAuthoritiesLifecycleStateEnum(
				strings.ToUpper(opt.state))
		}

		resp, err := client.ListCertificateAuthorities(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("list CAs in %s: %w", compartmentID, err)
		}
		out = append(out, resp.CertificateAuthorityCollection.Items...)

		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			return out, nil
		}
		page = resp.OpcNextPage
	}
}

// listCompartments returns the tenancy root plus every active compartment in
// the subtree the caller can see.
func listCompartments(
	ctx context.Context,
	provider common.ConfigurationProvider,
	region string,
	tenancyID string,
	retry *common.RetryPolicy,
) ([]string, error) {
	idc, err := identity.NewIdentityClientWithConfigurationProvider(provider)
	if err != nil {
		return nil, fmt.Errorf("build identity client: %w", err)
	}
	idc.SetRegion(region)

	ids := []string{tenancyID}
	var page *string
	for {
		resp, err := idc.ListCompartments(ctx, identity.ListCompartmentsRequest{
			CompartmentId:          common.String(tenancyID),
			CompartmentIdInSubtree: common.Bool(true),
			AccessLevel:            identity.ListCompartmentsAccessLevelAccessible,
			LifecycleState:         identity.CompartmentLifecycleStateActive,
			Limit:                  common.Int(100),
			Page:                   page,
			RequestMetadata:        common.RequestMetadata{RetryPolicy: retry},
		})
		if err != nil {
			return nil, fmt.Errorf("list compartments: %w", err)
		}
		for _, c := range resp.Items {
			ids = append(ids, deref(c.Id))
		}
		if resp.OpcNextPage == nil || *resp.OpcNextPage == "" {
			return ids, nil
		}
		page = resp.OpcNextPage
	}
}

func render(cas []certificatesmanagement.CertificateAuthoritySummary, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(cas)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATE\tCONFIG TYPE\tCOMMON NAME\tVER\tNOT AFTER\tOCID")
	for _, ca := range cas {
		var ver, notAfter string
		if v := ca.CurrentVersionSummary; v != nil {
			if v.VersionNumber != nil {
				ver = fmt.Sprintf("%d", *v.VersionNumber)
			}
			if v.Validity != nil && v.Validity.TimeOfValidityNotAfter != nil {
				notAfter = v.Validity.TimeOfValidityNotAfter.Format(time.RFC3339)
			}
		}
		cn := ""
		if ca.Subject != nil {
			cn = deref(ca.Subject.CommonName)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			deref(ca.Name), ca.LifecycleState, ca.ConfigType,
			cn, dash(ver), dash(notAfter), deref(ca.Id))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\n%d certificate authorities\n", len(cas))
	return nil
}

func isAuthzOrNotFound(err error) bool {
	if svcErr, ok := common.IsServiceError(err); ok {
		switch svcErr.GetHTTPStatusCode() {
		case 401, 403, 404:
			return true
		}
	}
	return false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
