package cluster

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

var ErrIncompleteDiscovery = errors.New("discovery was incomplete")

// Resource describes one namespaced, listable API resource as the live
// cluster reports it. SingularName and ShortNames exist so ResolveResource
// can match what a user typed against what the server actually calls the
// resource, instead of trusting a caller's guessed plural.
type Resource struct {
	GVR          schema.GroupVersionResource
	Kind         string
	Namespaced   bool
	SingularName string
	ShortNames   []string
}

// discoveryFailure adapts whatever discovery returns into a named set of
// groups, so the refusal can say which ones failed rather than "something".
type discoveryFailure struct{ groups []string }

func (d *discoveryFailure) Error() string { return strings.Join(d.groups, ", ") }

func wrapDiscoveryError(err error) error {
	if err == nil {
		return nil
	}
	var g *discovery.ErrGroupDiscoveryFailed
	if errors.As(err, &g) {
		var names []string
		for gv := range g.Groups {
			names = append(names, gv.String())
		}
		sort.Strings(names)
		return fmt.Errorf("%w: these api groups did not answer, so the enumeration would be short and would read as safe: %s",
			ErrIncompleteDiscovery, strings.Join(names, ", "))
	}
	var df *discoveryFailure
	if errors.As(err, &df) {
		return fmt.Errorf("%w: these api groups did not answer, so the enumeration would be short and would read as safe: %s",
			ErrIncompleteDiscovery, df.Error())
	}
	return fmt.Errorf("%w: %v", ErrIncompleteDiscovery, err)
}

func selectListable(lists []*metav1.APIResourceList) ([]Resource, error) {
	var out []Resource
	for _, l := range lists {
		gv, err := schema.ParseGroupVersion(l.GroupVersion)
		if err != nil {
			return nil, fmt.Errorf("api group %q is not parseable: %w", l.GroupVersion, err)
		}
		for _, r := range l.APIResources {
			// A subresource ("pods/log") is not an object and cannot be listed.
			if strings.Contains(r.Name, "/") {
				continue
			}
			if !r.Namespaced {
				continue
			}
			if !hasVerb(r.Verbs, "list") {
				continue
			}
			out = append(out, Resource{
				GVR:          gv.WithResource(r.Name),
				Kind:         r.Kind,
				Namespaced:   true,
				SingularName: r.SingularName,
				ShortNames:   r.ShortNames,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GVR.String() < out[j].GVR.String() })
	return out, nil
}

func hasVerb(verbs metav1.Verbs, want string) bool {
	for _, v := range verbs {
		if v == want {
			return true
		}
	}
	return false
}

// ListableNamespaced asks the live cluster for every namespaced resource that
// supports list. ServerPreferredNamespacedResourcesWithContext can return a
// partial list ALONGSIDE a non-nil error when one API group fails to answer
// -- a broken metrics-server or a stale APIService does this on clusters that
// otherwise look healthy. Returning the partial list here would make the
// enumeration downstream shorter, and shorter reads as safer, precisely when
// the cluster is least understood. So the error is fatal, full stop.
func ListableNamespaced(ctx context.Context, c *Clients) ([]Resource, error) {
	lists, err := discovery.ServerPreferredNamespacedResourcesWithContext(ctx, c.Discovery)
	if err != nil {
		return nil, wrapDiscoveryError(err)
	}
	return selectListable(lists)
}

// ResolveResource matches a user-supplied resource string against the live
// discovery data, which carries each resource's real plural, singular and
// short names. The action parser that produces that string does only naive
// plural normalisation (it cannot tell "ingress" from "ingresss"), so this is
// where the real resolution happens. It refuses when the match is not
// exactly one, naming the candidates -- guessing which resource was meant is
// how a tool ends up scoring the wrong object.
func ResolveResource(rs []Resource, s string) (Resource, error) {
	var matches []Resource
	for _, r := range rs {
		matched := strings.EqualFold(r.GVR.Resource, s) || strings.EqualFold(r.SingularName, s)
		if !matched {
			for _, sn := range r.ShortNames {
				if strings.EqualFold(sn, s) {
					matched = true
					break
				}
			}
		}
		if matched {
			matches = append(matches, r)
		}
	}

	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return Resource{}, fmt.Errorf("no resource matches %q", s)
	default:
		names := make([]string, len(matches))
		for i, m := range matches {
			names[i] = m.GVR.String()
		}
		return Resource{}, fmt.Errorf("%q matches more than one resource, refusing to guess: %s", s, strings.Join(names, ", "))
	}
}
