package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"singularity-vpn-app/internal/api"
	"singularity-vpn-app/internal/ipc"
)

func Test_parse_service_host(t *testing.T) {
	long_label := strings.Repeat("a", 63)
	cases := []struct {
		name string
		in   string
		host string
		port int
		err  bool
	}{
		{name: "a plain host", in: "example.com", host: "example.com", port: 443},
		{name: "trimmed and lowercased", in: "  Example.COM \t", host: "example.com", port: 443},
		{name: "an https url with a path", in: "https://example.com/path?x=1#y", host: "example.com", port: 443},
		{name: "an http url", in: "http://example.com", host: "example.com", port: 443},
		{name: "a scheme in capitals", in: "HTTPS://Example.com/", host: "example.com", port: 443},
		{name: "a port", in: "example.com:8443", host: "example.com", port: 8443},
		{name: "a port in a url", in: "https://example.com:8443/a/b", host: "example.com", port: 8443},
		{name: "the lowest port", in: "example.com:1", host: "example.com", port: 1},
		{name: "the highest port", in: "example.com:65535", host: "example.com", port: 65535},
		{name: "subdomains", in: "a.b.c.example.co.uk", host: "a.b.c.example.co.uk", port: 443},
		{name: "hyphens inside labels", in: "my-site.exa-mple.com", host: "my-site.exa-mple.com", port: 443},
		{name: "digits in labels", in: "3d.example.com", host: "3d.example.com", port: 443},
		{name: "punycode", in: "xn--e1afmkfd.xn--p1ai", host: "xn--e1afmkfd.xn--p1ai", port: 443},
		{name: "a label of 63", in: long_label + ".com", host: long_label + ".com", port: 443},
		{name: "a name of 253", in: strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61),
			host: strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 61), port: 443},

		{name: "empty", in: "", err: true},
		{name: "blank", in: "   ", err: true},
		{name: "only a scheme", in: "https://", err: true},
		{name: "no dot", in: "localhost", err: true},
		{name: "a single label with a port", in: "intranet:8080", err: true},
		{name: "port zero", in: "example.com:0", err: true},
		{name: "port too high", in: "example.com:65536", err: true},
		{name: "port not a number", in: "example.com:abc", err: true},
		{name: "port signed", in: "example.com:+80", err: true},
		{name: "port empty", in: "example.com:", err: true},
		{name: "two ports", in: "example.com:80:90", err: true},
		{name: "an empty label", in: "example..com", err: true},
		{name: "a leading dot", in: ".example.com", err: true},
		{name: "a trailing dot", in: "example.com.", err: true},
		{name: "a label starting with a hyphen", in: "-bad.example.com", err: true},
		{name: "a label ending with a hyphen", in: "bad-.example.com", err: true},
		{name: "a space inside", in: "exa mple.com", err: true},
		{name: "an underscore", in: "exa_mple.com", err: true},
		{name: "non-ascii", in: "пример.рф", err: true},
		{name: "an ipv4 address", in: "93.184.216.34", err: true},
		{name: "an ipv4 address with a port", in: "93.184.216.34:443", err: true},
		{name: "an ipv6 address", in: "[2001:db8::1]:443", err: true},
		{name: "userinfo", in: "user@example.com", err: true},
		{name: "a query without a path", in: "example.com?x=1", err: true},
		{name: "another scheme", in: "ftp://example.com", err: true},
		{name: "a label of 64", in: strings.Repeat("a", 64) + ".com", err: true},
		{name: "a name of 254", in: strings.Repeat("a", 63) + "." + strings.Repeat("b", 63) + "." + strings.Repeat("c", 63) + "." + strings.Repeat("d", 62), err: true},
		{name: "a numeric top level", in: "example.123", err: true},
		{name: "a hex-looking top level", in: "127.0.0.0x1", err: true},
		{name: "a control character", in: "example.com\x00", err: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			host, port, err := parse_service_host(c.in)
			if c.err {
				if !errors.Is(err, ipc.Err_invalid_host) {
					t.Fatalf("parse_service_host(%q) = %q, %d, %v; want invalid_host", c.in, host, port, err)
				}
				return
			}
			if err != nil || host != c.host || port != c.port {
				t.Fatalf("parse_service_host(%q) = %q, %d, %v; want %q, %d", c.in, host, port, err, c.host, c.port)
			}
		})
	}
}

// reach_catalog_targets is a control inside the country and two popular
// services.
func reach_catalog_targets() []api.Reach_target {
	return []api.Reach_target{
		{Key: "bank_ru", Host: "bank.example", Port: 443, Country: "RU", Label: "Bank"},
		{Key: "web_youtube", Host: "www.youtube.com", Port: 443, Label: "YouTube"},
		{Key: "web_x", Host: "x.com", Port: 443, Label: "X"},
	}
}

func service_keys(list ipc.Services_result) string {
	keys := make([]string, 0, len(list.Targets))
	for _, t := range list.Targets {
		keys = append(keys, t.Key)
	}
	return strings.Join(keys, ",")
}

func Test_the_effective_list(t *testing.T) {
	catalog := reach_catalog_targets()
	cases := []struct {
		name    string
		edits   reach_edits
		keys    string
		removed int
	}{
		{"no edits", reach_edits{}, "bank_ru,web_youtube,web_x", 0},
		{"a popular service removed", reach_edits{Removed: []string{"web_youtube"}}, "bank_ru,web_x", 1},
		{"every popular service removed", reach_edits{Removed: []string{"web_youtube", "web_x"}}, "bank_ru", 2},
		{"a control named among the removed stays", reach_edits{Removed: []string{"bank_ru"}}, "bank_ru,web_youtube,web_x", 0},
		{"a removed key the catalog no longer has counts for nothing", reach_edits{Removed: []string{"web_gone"}}, "bank_ru,web_youtube,web_x", 0},
		{"a host added", reach_edits{Added: []reach_added{{Host: "example.org", Port: 443}}}, "bank_ru,web_youtube,web_x,custom:example.org:443", 0},
		{"hosts added keep their order", reach_edits{Added: []reach_added{{Host: "b.example.org", Port: 8443}, {Host: "a.example.org", Port: 443}}},
			"bank_ru,web_youtube,web_x,custom:b.example.org:8443,custom:a.example.org:443", 0},
		{"removed and added", reach_edits{Removed: []string{"web_x"}, Added: []reach_added{{Host: "example.org", Port: 443}}}, "bank_ru,web_youtube,custom:example.org:443", 1},
		{"an added host the catalog now carries itself is left to the catalog", reach_edits{Added: []reach_added{{Host: "X.com", Port: 443}}}, "bank_ru,web_youtube,web_x", 0},
		{"an added host on another port of a catalog host is its own entry", reach_edits{Added: []reach_added{{Host: "x.com", Port: 8443}}}, "bank_ru,web_youtube,web_x,custom:x.com:8443", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			effective, list := compose_reach_list(catalog, c.edits)
			if got := service_keys(list); got != c.keys {
				t.Fatalf("list = %s, want %s", got, c.keys)
			}
			if list.Removed_popular != c.removed {
				t.Fatalf("removed_popular = %d, want %d", list.Removed_popular, c.removed)
			}
			probed := make([]string, 0, len(effective))
			for _, e := range effective {
				probed = append(probed, e.Key)
			}
			if got := strings.Join(probed, ","); got != c.keys {
				t.Fatalf("probed = %s, want the list shown, %s", got, c.keys)
			}
		})
	}

	t.Run("the shape of an entry", func(t *testing.T) {
		_, list := compose_reach_list(catalog, reach_edits{Added: []reach_added{{Host: "example.org", Port: 8443}}})
		want := []ipc.Service_target{
			{Key: "bank_ru", Label: "Bank", Host: "bank.example", Port: 443, Popular: true, Control: true},
			{Key: "web_youtube", Label: "YouTube", Host: "www.youtube.com", Port: 443, Popular: true},
			{Key: "web_x", Label: "X", Host: "x.com", Port: 443, Popular: true},
			{Key: "custom:example.org:8443", Label: "example.org", Host: "example.org", Port: 8443},
		}
		if fmt.Sprint(list.Targets) != fmt.Sprint(want) {
			t.Fatalf("targets = %+v, want %+v", list.Targets, want)
		}
	})
	t.Run("a catalog entry without a label is shown by its host", func(t *testing.T) {
		_, list := compose_reach_list([]api.Reach_target{{Key: "k", Host: "nolabel.example", Port: 443}}, reach_edits{})
		if list.Targets[0].Label != "nolabel.example" {
			t.Fatalf("label = %q", list.Targets[0].Label)
		}
	})
}

func Test_editing_the_list(t *testing.T) {
	catalog := reach_catalog_targets()
	type step struct {
		do   func(reach_edits) (reach_edits, error)
		want error // nil: the edit applies
		list string
		gone int // removed_popular afterwards
	}
	add := func(host string) func(reach_edits) (reach_edits, error) {
		return func(e reach_edits) (reach_edits, error) { return edit_add(catalog, e, host) }
	}
	remove := func(key string) func(reach_edits) (reach_edits, error) {
		return func(e reach_edits) (reach_edits, error) { return edit_remove(catalog, e, key) }
	}
	restore := func(e reach_edits) (reach_edits, error) { return edit_restore(catalog, e) }

	cases := []struct {
		name  string
		steps []step
	}{
		{"remove a popular service, then restore", []step{
			{do: remove("web_youtube"), list: "bank_ru,web_x", gone: 1},
			{do: restore, list: "bank_ru,web_youtube,web_x", gone: 0},
		}},
		{"removing twice is the same as once", []step{
			{do: remove("web_x"), list: "bank_ru,web_youtube", gone: 1},
			{do: remove("web_x"), list: "bank_ru,web_youtube", gone: 1},
		}},
		{"a control cannot be removed", []step{
			{do: remove("bank_ru"), want: ipc.Err_service_not_removable, list: "bank_ru,web_youtube,web_x"},
		}},
		{"an unknown key cannot be removed", []step{
			{do: remove("web_nothing"), want: ipc.Err_unknown_service, list: "bank_ru,web_youtube,web_x"},
			{do: remove("custom:nowhere.example:443"), want: ipc.Err_unknown_service, list: "bank_ru,web_youtube,web_x"},
		}},
		{"add a host, remove it by its key", []step{
			{do: add("https://Example.org/page"), list: "bank_ru,web_youtube,web_x,custom:example.org:443"},
			{do: remove("custom:example.org:443"), list: "bank_ru,web_youtube,web_x"},
		}},
		{"an added host is not a duplicate of another port", []step{
			{do: add("example.org"), list: "bank_ru,web_youtube,web_x,custom:example.org:443"},
			{do: add("example.org:8443"), list: "bank_ru,web_youtube,web_x,custom:example.org:443,custom:example.org:8443"},
		}},
		{"the same host twice is a duplicate", []step{
			{do: add("example.org"), list: "bank_ru,web_youtube,web_x,custom:example.org:443"},
			{do: add("http://EXAMPLE.org/other"), want: ipc.Err_duplicate_service, list: "bank_ru,web_youtube,web_x,custom:example.org:443"},
		}},
		{"a host the catalog carries is a duplicate", []step{
			{do: add("www.youtube.com"), want: ipc.Err_duplicate_service, list: "bank_ru,web_youtube,web_x"},
		}},
		{"a control's host is a duplicate even after a try to remove it", []step{
			{do: remove("bank_ru"), want: ipc.Err_service_not_removable, list: "bank_ru,web_youtube,web_x"},
			{do: add("bank.example"), want: ipc.Err_duplicate_service, list: "bank_ru,web_youtube,web_x"},
		}},
		{"adding a removed popular host puts it back and adds nothing", []step{
			{do: remove("web_youtube"), list: "bank_ru,web_x", gone: 1},
			{do: add("https://www.youtube.com/"), list: "bank_ru,web_youtube,web_x", gone: 0},
			{do: add("www.youtube.com"), want: ipc.Err_duplicate_service, list: "bank_ru,web_youtube,web_x"},
		}},
		{"restoring keeps what was added", []step{
			{do: add("example.org"), list: "bank_ru,web_youtube,web_x,custom:example.org:443"},
			{do: remove("web_x"), list: "bank_ru,web_youtube,custom:example.org:443", gone: 1},
			{do: restore, list: "bank_ru,web_youtube,web_x,custom:example.org:443", gone: 0},
		}},
		{"an invalid host changes nothing", []step{
			{do: add("not a host"), want: ipc.Err_invalid_host, list: "bank_ru,web_youtube,web_x"},
			{do: add(""), want: ipc.Err_invalid_host, list: "bank_ru,web_youtube,web_x"},
			{do: add("localhost"), want: ipc.Err_invalid_host, list: "bank_ru,web_youtube,web_x"},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			edits := sanitize_reach_edits(reach_edits{})
			for i, s := range c.steps {
				next, err := s.do(edits)
				if s.want != nil {
					if !errors.Is(err, s.want) {
						t.Fatalf("step %d: error = %v, want %v", i, err, s.want)
					}
					if fmt.Sprint(next) != fmt.Sprint(edits) {
						t.Fatalf("step %d: a refused edit returned %+v, want the edits as they were, %+v", i, next, edits)
					}
				} else if err != nil {
					t.Fatalf("step %d: %v", i, err)
				} else {
					edits = next
				}
				_, list := compose_reach_list(catalog, edits)
				if got := service_keys(list); got != s.list || list.Removed_popular != s.gone {
					t.Fatalf("step %d: list = %s (removed %d), want %s (removed %d)", i, got, list.Removed_popular, s.list, s.gone)
				}
			}
		})
	}

	t.Run("at most thirty hosts", func(t *testing.T) {
		edits := sanitize_reach_edits(reach_edits{})
		for i := 0; i < reach_added_max; i++ {
			next, err := edit_add(catalog, edits, fmt.Sprintf("site%d.example.org", i))
			if err != nil {
				t.Fatalf("host %d: %v", i, err)
			}
			edits = next
		}
		if _, err := edit_add(catalog, edits, "one-more.example.org"); !errors.Is(err, ipc.Err_too_many_services) {
			t.Fatalf("host %d: %v, want too_many", reach_added_max+1, err)
		}
		// A duplicate is still a duplicate, and a restored popular host is not an addition.
		if _, err := edit_add(catalog, edits, "site3.example.org"); !errors.Is(err, ipc.Err_duplicate_service) {
			t.Fatalf("a duplicate in a full list: %v, want duplicate", err)
		}
		edits, _ = edit_remove(catalog, edits, "web_x")
		if _, err := edit_add(catalog, edits, "x.com"); err != nil {
			t.Fatalf("putting a removed popular host back in a full list: %v", err)
		}
	})
}

func Test_stored_edits_are_sanitised(t *testing.T) {
	in := reach_edits{
		Removed: []string{"a", "", "a", strings.Repeat("k", 301), "b"},
		Added: []reach_added{
			{Host: "ok.example.org", Port: 443},
			{Host: "ok.example.org", Port: 443},
			{Host: "UPPER.example.org", Port: 443},
			{Host: "nodot", Port: 443},
			{Host: "bad port.example.org", Port: 443},
			{Host: "zero.example.org", Port: 0},
			{Host: "high.example.org", Port: 70000},
			{Host: "ok.example.org", Port: 8443},
		},
	}
	out := sanitize_reach_edits(in)
	if strings.Join(out.Removed, ",") != "a,b" {
		t.Fatalf("removed = %v", out.Removed)
	}
	if fmt.Sprint(out.Added) != fmt.Sprint([]reach_added{{"ok.example.org", 443}, {"ok.example.org", 8443}}) {
		t.Fatalf("added = %v", out.Added)
	}

	many := reach_edits{}
	for i := 0; i < reach_added_max+10; i++ {
		many.Added = append(many.Added, reach_added{Host: fmt.Sprintf("s%d.example.org", i), Port: 443})
	}
	if got := len(sanitize_reach_edits(many).Added); got != reach_added_max {
		t.Fatalf("%d hosts kept, want %d", got, reach_added_max)
	}

	raw, _ := json.Marshal(sanitize_reach_edits(reach_edits{}))
	if string(raw) != `{"removed":[],"added":[]}` {
		t.Fatalf("empty edits are written as %s, want empty lists and not null", raw)
	}
}

// --- through the application -----------------------------------------------

func with_reach_catalog(f *fixture) {
	f.control_plane.catalog = api.Reach_catalog{Country: "RU", Targets: reach_catalog_targets()}
}

func Test_the_list_commands_through_the_application(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_reach_catalog(f)
	ctx := context.Background()

	list, err := f.app.Services(ctx)
	if err != nil || service_keys(list) != "bank_ru,web_youtube,web_x" || list.Removed_popular != 0 {
		t.Fatalf("Services() = %+v, %v", list, err)
	}
	list, err = f.app.Remove_service(ctx, ipc.Remove_service_params{Key: "web_youtube"})
	if err != nil || service_keys(list) != "bank_ru,web_x" || list.Removed_popular != 1 {
		t.Fatalf("Remove_service() = %+v, %v", list, err)
	}
	list, err = f.app.Add_service(ctx, ipc.Add_service_params{Host: "https://Example.org/x"})
	if err != nil || service_keys(list) != "bank_ru,web_x,custom:example.org:443" || list.Removed_popular != 1 {
		t.Fatalf("Add_service() = %+v, %v", list, err)
	}
	if _, err := f.app.Add_service(ctx, ipc.Add_service_params{Host: "example.org"}); !errors.Is(err, ipc.Err_duplicate_service) {
		t.Fatalf("Add_service(duplicate) = %v", err)
	}
	if _, err := f.app.Add_service(ctx, ipc.Add_service_params{Host: "nonsense"}); !errors.Is(err, ipc.Err_invalid_host) {
		t.Fatalf("Add_service(nonsense) = %v", err)
	}
	if _, err := f.app.Remove_service(ctx, ipc.Remove_service_params{Key: "bank_ru"}); !errors.Is(err, ipc.Err_service_not_removable) {
		t.Fatalf("Remove_service(control) = %v", err)
	}
	if _, err := f.app.Remove_service(ctx, ipc.Remove_service_params{Key: "nothing"}); !errors.Is(err, ipc.Err_unknown_service) {
		t.Fatalf("Remove_service(unknown) = %v", err)
	}
	list, err = f.app.Restore_services(ctx)
	if err != nil || service_keys(list) != "bank_ru,web_youtube,web_x,custom:example.org:443" || list.Removed_popular != 0 {
		t.Fatalf("Restore_services() = %+v, %v", list, err)
	}
}

func Test_an_analysis_probes_the_list_as_edited(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f) // bank_ru, web_youtube
	ctx := context.Background()
	if _, err := f.app.Remove_service(ctx, ipc.Remove_service_params{Key: "web_youtube"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Add_service(ctx, ipc.Add_service_params{Host: "example.org:8443"}); err != nil {
		t.Fatal(err)
	}

	if _, err := f.app.Analyze(ctx); err != nil {
		t.Fatal(err)
	}
	var asked []string
	for _, target := range f.reach_prober.targets_asked() {
		asked = append(asked, fmt.Sprintf("%s|%s|%d|%s|%s", target.Key, target.Host, target.Port, target.Country, target.Label))
	}
	want := "bank_ru|bank.example|443|RU|Bank,custom:example.org:8443|example.org|8443||example.org"
	if got := strings.Join(asked, ","); got != want {
		t.Fatalf("probed %s, want %s", got, want)
	}

	// The catalog failing still fails the run: the list is the catalog's.
	f.control_plane.catalog_err = &api.Error{Code: api.Code_unauthorized}
	f.app.analysis.mu.Lock()
	f.app.analysis.stale = true
	f.app.analysis.mu.Unlock()
	if _, err := f.app.Analyze(ctx); !errors.Is(err, ipc.Err_unauthorized) {
		t.Fatalf("Analyze() with the catalog refused = %v", err)
	}
}

func Test_the_list_commands_need_the_catalog_and_change_nothing_without_it(t *testing.T) {
	f := new_fixture(t)
	with_reach_catalog(f)
	ctx := context.Background()

	if _, err := f.app.Services(ctx); !errors.Is(err, ipc.Err_not_signed_in) {
		t.Fatalf("Services() before sign-in = %v", err)
	}
	sign_in(t, f)
	if _, err := f.app.Add_service(ctx, ipc.Add_service_params{Host: "example.org"}); err != nil {
		t.Fatal(err)
	}

	f.control_plane.catalog_err = &api.Error{Code: api.Code_unauthorized}
	calls := []struct {
		name string
		do   func() error
	}{
		{"Services", func() error { _, err := f.app.Services(ctx); return err }},
		{"Remove_service", func() error {
			_, err := f.app.Remove_service(ctx, ipc.Remove_service_params{Key: "web_x"})
			return err
		}},
		{"Add_service", func() error {
			_, err := f.app.Add_service(ctx, ipc.Add_service_params{Host: "other.example.org"})
			return err
		}},
		{"Restore_services", func() error { _, err := f.app.Restore_services(ctx); return err }},
	}
	for _, c := range calls {
		if err := c.do(); !errors.Is(err, ipc.Err_unauthorized) {
			t.Fatalf("%s() with the catalog out of reach = %v, want the catalog's error", c.name, err)
		}
	}

	f.control_plane.catalog_err = nil
	list, err := f.app.Services(ctx)
	if err != nil || service_keys(list) != "bank_ru,web_youtube,web_x,custom:example.org:443" {
		t.Fatalf("Services() afterwards = %+v, %v; want the list as it was", list, err)
	}
}

func Test_the_edits_survive_a_restart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reach-list.json")
	ctx := context.Background()

	first := new_fixture(t)
	sign_in(t, first)
	with_reach_catalog(first)
	first.app.reach_list_path = path
	if _, err := first.app.Remove_service(ctx, ipc.Remove_service_params{Key: "web_youtube"}); err != nil {
		t.Fatal(err)
	}
	if _, err := first.app.Add_service(ctx, ipc.Add_service_params{Host: "Example.org:8443"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != `{"removed":["web_youtube"],"added":[{"host":"example.org","port":8443}]}` {
		t.Fatalf("the file holds %s, %v", raw, err)
	}

	second := new_fixture(t)
	sign_in(t, second)
	with_reach_catalog(second)
	second.app.reach_list_path = path
	list, err := second.app.Services(ctx)
	if err != nil || service_keys(list) != "bank_ru,web_x,custom:example.org:8443" || list.Removed_popular != 1 {
		t.Fatalf("Services() after a restart = %+v, %v", list, err)
	}
	if entries, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".reach-list-*")); len(entries) != 0 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func Test_a_missing_or_damaged_list_file_is_the_catalog_unedited(t *testing.T) {
	good := `{"removed":["web_x"],"added":[{"host":"example.org","port":443}]}`
	cases := []struct {
		name     string
		contents *string
		keys     string
		warning  string
	}{
		{"no file", nil, "bank_ru,web_youtube,web_x", ""},
		{"a good file", &good, "bank_ru,web_youtube,custom:example.org:443", ""},
		{"not json", ptr("SECRET-NOT-JSON"), "bank_ru,web_youtube,web_x", "service list file invalid"},
		{"the wrong shape", ptr(`["SECRET"]`), "bank_ru,web_youtube,web_x", "service list file invalid"},
		{"empty", ptr(""), "bank_ru,web_youtube,web_x", "service list file invalid"},
		{"too large", ptr(strings.Repeat(" SECRET", reach_list_limit)), "bank_ru,web_youtube,web_x", "service list file unavailable"},
		{"bad entries are dropped, the rest stays", ptr(`{"removed":["web_x",""],"added":[{"host":"not a host","port":443},{"host":"fine.example.org","port":443}]}`),
			"bank_ru,web_youtube,custom:fine.example.org:443", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := new_fixture(t)
			sign_in(t, f)
			with_reach_catalog(f)
			logs := logging(f)
			path := filepath.Join(t.TempDir(), "reach-list.json")
			f.app.reach_list_path = path
			if c.contents != nil {
				if err := os.WriteFile(path, []byte(*c.contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			list, err := f.app.Services(context.Background())
			if err != nil || service_keys(list) != c.keys {
				t.Fatalf("Services() = %s, %v; want %s", service_keys(list), err, c.keys)
			}
			if c.warning == "" && strings.Contains(logs.String(), "level=WARN") {
				t.Fatalf("unexpected warning: %s", logs.String())
			}
			if c.warning != "" && !strings.Contains(logs.String(), c.warning) {
				t.Fatalf("log = %q, want %q", logs.String(), c.warning)
			}
			if strings.Contains(logs.String(), "SECRET") {
				t.Fatalf("the log quotes the file: %s", logs.String())
			}
			// A damaged file is replaced by the next edit.
			if _, err := f.app.Restore_services(context.Background()); err != nil {
				t.Fatal(err)
			}
			var stored reach_edits
			raw, _ := os.ReadFile(path)
			if json.Unmarshal(raw, &stored) != nil {
				t.Fatalf("the file after an edit is %q, want valid json", raw)
			}
		})
	}
}

func ptr(s string) *string { return &s }

func Test_an_edit_that_cannot_be_saved_changes_nothing(t *testing.T) {
	f := new_fixture(t)
	sign_in(t, f)
	with_reach_catalog(f)
	f.app.reach_list_path = filepath.Join(t.TempDir(), "no-such-directory", "reach-list.json")

	if _, err := f.app.Add_service(context.Background(), ipc.Add_service_params{Host: "example.org"}); err == nil {
		t.Fatal("Add_service() into a directory that is not there = nil")
	}
	list, err := f.app.Services(context.Background())
	if err != nil || service_keys(list) != "bank_ru,web_youtube,web_x" {
		t.Fatalf("Services() after a failed save = %s, %v; want the list as it was", service_keys(list), err)
	}
}

func Test_a_list_command_does_not_wait_for_a_run_in_flight(t *testing.T) {
	// A list command fetches the catalog and touches its own file; it must not
	// queue behind a measurement.
	f := new_fixture(t)
	sign_in(t, f)
	with_catalog(f)
	answer := start_blocked_analysis(t, f)
	done := make(chan error, 1)
	go func() {
		_, err := f.app.Add_service(context.Background(), ipc.Add_service_params{Host: "example.org"})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a list command waited for a run in flight")
	}
	close(f.reach_prober.release)
	if err := await_answer(t, answer); err != nil {
		t.Fatal(err)
	}
}
