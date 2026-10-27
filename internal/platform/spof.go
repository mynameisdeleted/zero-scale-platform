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
}

// Survives reports whether the Service keeps serving when any one node is lost.
func (e Exposure) Survives() bool { return len(e.Nodes) >= 2 }

// Problem describes why the Service would not survive, or "" when it would.
func (e Exposure) Problem() string {
	switch {
	case e.Pods == 0:
		return "no running pods"
	case len(e.Nodes) < 2 && e.Pods == 1:
		return "1 pod, on " + e.Nodes[0]
	case len(e.Nodes) < 2:
		return fmt.Sprintf("%d pods, all on %s", e.Pods, e.Nodes[0])
	}
	return ""
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
		sel := selectors[key]
		if len(sel) == 0 {
			continue // no Service, or one without a selector (external endpoints): not judged
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
			}
		}
		for n := range nodes {
			e.Nodes = append(e.Nodes, n)
		}
		sort.Strings(e.Nodes)
	}

	var out []Exposure
	for key, e := range byKey {
		if len(selectors[key]) == 0 {
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
