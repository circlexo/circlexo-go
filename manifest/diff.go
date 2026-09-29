package manifest

import (
	"sort"
	"strconv"
	"strings"
)

// Change is one way a new version asks for more than the previous one. Such
// changes need a console review and org re-consent (design §5).
type Change struct {
	Kind  string `json:"kind"` // scope | redirect_uri | webhook | webhook_event | destructive_tool | service_account
	Value string `json:"value"`
}

func (c Change) String() string { return c.Kind + ":" + c.Value }

// Widening lists what next asks for beyond prev. A nil prev is a first
// version: everything it asks for is new.
func Widening(prev, next *Manifest) []Change {
	if prev == nil {
		prev = &Manifest{}
	}
	var out []Change
	added := func(kind string, old, cur []string) {
		have := map[string]bool{}
		for _, v := range old {
			have[v] = true
		}
		for _, v := range cur {
			if !have[v] {
				out = append(out, Change{kind, v})
			}
		}
	}
	added("scope", prev.Auth.Scopes, next.Auth.Scopes)
	added("redirect_uri", prev.Auth.RedirectURIs, next.Auth.RedirectURIs)
	if next.Auth.ServiceAccount && !prev.Auth.ServiceAccount {
		out = append(out, Change{"service_account", "on"})
	}
	if w := next.Webhooks; w != nil {
		var oldURL string
		var oldEvents []string
		if prev.Webhooks != nil {
			oldURL, oldEvents = prev.Webhooks.URL, prev.Webhooks.Events
		}
		if w.URL != oldURL {
			out = append(out, Change{"webhook", w.URL})
		}
		added("webhook_event", oldEvents, w.Events)
	}
	var oldDestructive []string
	if prev.MCP != nil {
		oldDestructive = prev.Tools(EffectDestructive)
	}
	added("destructive_tool", oldDestructive, next.Tools(EffectDestructive))
	sort.SliceStable(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// CompareVersions orders two semantic versions: -1, 0 or 1. A pre-release
// sorts before its release; build metadata is ignored. Invalid input sorts
// first.
func CompareVersions(a, b string) int {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	switch {
	case !oka && !okb:
		return strings.Compare(a, b)
	case !oka:
		return -1
	case !okb:
		return 1
	}
	for i := 0; i < 3; i++ {
		if pa.n[i] != pb.n[i] {
			if pa.n[i] < pb.n[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case pa.pre == pb.pre:
		return 0
	case pa.pre == "":
		return 1
	case pb.pre == "":
		return -1
	}
	return comparePre(pa.pre, pb.pre)
}

type semver struct {
	n   [3]int
	pre string
}

func parseSemver(v string) (semver, bool) {
	var s semver
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		v, s.pre = v[:i], v[i+1:]
	}
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return s, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return s, false
		}
		s.n[i] = n
	}
	return s, true
}

// comparePre follows semver §11: numeric identifiers compare as numbers and
// sort before alphanumeric ones; a shorter list sorts first.
func comparePre(a, b string) int {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
		case aerr == nil:
			return -1
		case berr == nil:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}
