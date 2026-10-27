package platform

import (
	"fmt"
	"sort"
	"strings"
)

// Exposure is one Service behind an Ingress, with where its pods run.
type Exposure struct {
	Namespace string
	Service   string
	Hosts     []string
	Pods      int      // running pods behind the Service
	Nodes     []string // distinct nodes those pods run on
	Missing   bool     // the Ingress names a Service that does not exist
	Pinned    []string // node-local PVCs with persistent data that the pods mount
}

// Survives reports whether the Service keeps serving when any one node is lost.
func (e Exposure) Survives() bool { return !e.Missing && len(e.Nodes) >= 2 && len(e.Pinned) == 0 }

// Problem describes why the Service would not survive, or "" when it would.
func (e Exposure) Problem() string {
	var p string
	switch {
	case e.Missing:
		return "Service not found"
	case e.Pods == 0:
		return "no running pods"
	case len(e.Nodes) < 2 && e.Pods == 1:
		p = "1 pod, on " + e.Nodes[0]
	case len(e.Nodes) < 2:
		p = fmt.Sprintf("%d pods, all on %s", e.Pods, e.Nodes[0])
	}
	if len(e.Pinned) > 0 {
		if p != "" {
			p += "; "
		}
		p += "persistent data on node-local " + strings.Join(e.Pinned, ", ")
	}
	return p
}

// IngressExposures lists every Service an Ingress routes to and the nodes its
// running pods are on, so doctor can flag what one node failure takes down.
func IngressExposures(k Kube) ([]Exposure, error) {
	ingresses, err := getItems(k, "get", "ingress", "-A")
	if err != nil {
		return nil, err
	}
	services, err := getItems(k, "get", "service", "-A")
	if err != nil {
		return nil, err
	}
	pods, err := getItems(k, "get", "pods", "-A")
	if err != nil {
		return nil, err
	}
	locals, err := NodeLocalVolumes(k)
	if err != nil {
		return nil, err
	}
	atRisk := map[string]LocalVolume{}
	for _, v := range locals {
		if v.AtRisk() {
			atRisk[v.Namespace+"/"+v.PVC] = v
		}
	}

	byKey := map[string]*Exposure{}
	for _, ing := range ingresses {
		ns := ing.str("metadata", "namespace")
		add := func(svc, host string) {
			if svc == "" {
				return
			}
			key := ns + "/" + svc
			e := byKey[key]
			if e == nil {
				e = &Exposure{Namespace: ns, Service: svc}
				byKey[key] = e
			}
			if host != "" && !hasHost(e.Hosts, host) {
				e.Hosts = append(e.Hosts, host)
			}
		}
		add(ing.str("spec", "defaultBackend", "service", "name"), "*")
		for _, rule := range ing.list("spec", "rules") {
			for _, p := range rule.list("http", "paths") {
				add(p.str("backend", "service", "name"), rule.str("host"))
			}
		}
	}

	selectors := map[string]map[string]string{}
	for _, s := range services {
		sel := map[string]string{}
		if m, ok := s.get("spec", "selector").(map[string]any); ok {
			for k, v := range m {
				sel[k], _ = v.(string)
			}
		}
		selectors[s.str("metadata", "namespace")+"/"+s.str("metadata", "name")] = sel
	}

	for key, e := range byKey {
		sel, found := selectors[key]
		if !found {
			e.Missing = true
			continue
		}
		if len(sel) == 0 {
			continue // a Service without a selector (external endpoints): not judged
		}
		nodes := map[string]bool{}
		for _, p := range pods {
			if p.str("metadata", "namespace") != e.Namespace || p.str("status", "phase") != "Running" || p.get("metadata", "deletionTimestamp") != nil {
				continue
			}
			labels, _ := p.get("metadata", "labels").(map[string]any)
			match := true
			for k, v := range sel {
				if lv, _ := labels[k].(string); lv != v {
					match = false
					break
				}
			}
			if match {
				e.Pods++
				nodes[p.str("spec", "nodeName")] = true
				for _, vol := range p.list("spec", "volumes") {
					claim := vol.str("persistentVolumeClaim", "claimName")
					if v, ok := atRisk[e.Namespace+"/"+claim]; ok && !contains(e.Pinned, claim+" ("+v.Class+")") {
						e.Pinned = append(e.Pinned, claim+" ("+v.Class+")")
					}
				}
			}
		}
		for n := range nodes {
			e.Nodes = append(e.Nodes, n)
		}
		sort.Strings(e.Nodes)
	}

	var out []Exposure
	for key, e := range byKey {
		if sel, found := selectors[key]; found && len(sel) == 0 {
			continue
		}
		sort.Strings(e.Hosts)
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Namespace+"/"+out[i].Service < out[j].Namespace+"/"+out[j].Service
	})
	return out, nil
}

func hasHost(list []string, h string) bool {
	for _, x := range list {
		if strings.EqualFold(x, h) {
			return true
		}
	}
	return false
}

// IPClaim is a Service whose externalIPs include a node's own address. kube-proxy
// then sends that node's traffic on the Service's ports to the Service instead of
// to whatever listens on the node (such as ingress-nginx's hostPort).
type IPClaim struct {
	Namespace string
	Service   string
	IP        string
	Node      string
	Ports     []int
}

// NodeIPClaims lists Services that capture traffic addressed to a node IP.
func NodeIPClaims(k Kube) ([]IPClaim, error) {
	nodes, err := getItems(k, "get", "nodes")
	if err != nil {
		return nil, err
	}
	services, err := getItems(k, "get", "service", "-A")
	if err != nil {
		return nil, err
	}
	nodeByIP := map[string]string{}
	for _, n := range nodes {
		for _, a := range n.list("status", "addresses") {
			if t := a.str("type"); t == "InternalIP" || t == "ExternalIP" {
				nodeByIP[a.str("address")] = n.str("metadata", "name")
			}
		}
	}
	var out []IPClaim
	for _, s := range services {
		ips, _ := s.get("spec", "externalIPs").([]any)
		for _, v := range ips {
			ip, _ := v.(string)
			node, ok := nodeByIP[ip]
			if !ok {
				continue
			}
			c := IPClaim{Namespace: s.str("metadata", "namespace"), Service: s.str("metadata", "name"), IP: ip, Node: node}
			for _, p := range s.list("spec", "ports") {
				if f, ok := p.get("port").(float64); ok {
					c.Ports = append(c.Ports, int(f))
				}
			}
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Namespace+"/"+out[i].Service < out[j].Namespace+"/"+out[j].Service
	})
	return out, nil
}

// RouteConflict is one host and path that several Ingresses send to different Services.
type RouteConflict struct {
	Host, Path string
	Routes     []string // namespace/ingress -> service
}

// RouteConflicts lists host+path pairs routed to different backends by more than
// one Ingress; ingress-nginx then serves only one of them.
func RouteConflicts(k Kube) ([]RouteConflict, error) {
	ingresses, err := getItems(k, "get", "ingress", "-A")
	if err != nil {
		return nil, err
	}
	type route struct{ ingress, service string }
	byRoute := map[[2]string][]route{}
	for _, ing := range ingresses {
		ns := ing.str("metadata", "namespace")
		for _, rule := range ing.list("spec", "rules") {
			host := strings.ToLower(rule.str("host"))
			for _, p := range rule.list("http", "paths") {
				path := p.str("path")
				if path == "" {
					path = "/"
				}
				key := [2]string{host, path}
				byRoute[key] = append(byRoute[key], route{ns + "/" + ing.str("metadata", "name"), ns + "/" + p.str("backend", "service", "name")})
			}
		}
	}
	var out []RouteConflict
	for key, routes := range byRoute {
		services := map[string]bool{}
		for _, r := range routes {
			services[r.service] = true
		}
		if len(services) < 2 {
			continue
		}
		c := RouteConflict{Host: key[0], Path: key[1]}
		for _, r := range routes {
			c.Routes = append(c.Routes, r.ingress+" -> "+r.service)
		}
		sort.Strings(c.Routes)
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host+out[i].Path < out[j].Host+out[j].Path })
	return out, nil
}

// KnativeOnlyHosts lists Knative DomainMapping hosts that no Ingress rule
// routes. They only answer on the Knative gateway's own address (Kourier),
// not through the ingress controller on every node. A cluster without
// Knative yields nothing.
func KnativeOnlyHosts(k Kube) ([]string, error) {
	mappings, err := getItems(k, "get", "domainmappings.serving.knative.dev", "-A")
	if err != nil {
		return nil, nil // no Knative
	}
	ingresses, err := getItems(k, "get", "ingress", "-A")
	if err != nil {
		return nil, err
	}
	var routed []string
	for _, ing := range ingresses {
		for _, rule := range ing.list("spec", "rules") {
			routed = append(routed, rule.str("host"))
		}
	}
	var out []string
	for _, m := range mappings {
		if h := m.str("metadata", "name"); !hasHost(routed, h) && !hasHost(out, h) {
			out = append(out, h)
		}
	}
	sort.Strings(out)
	return out, nil
}
