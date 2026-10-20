package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// OAuthSetupSecretName returns the Kubernetes Secret name for a shared oauth-setup profile.
func OAuthSetupSecretName(setupName string) string {
	return fmt.Sprintf("zero-scale-oauth-%s", strings.ToLower(strings.TrimSpace(setupName)))
}

// OAuthSetupConfigMapName returns the Kubernetes ConfigMap name for a shared oauth-setup profile.
func OAuthSetupConfigMapName(setupName string) string {
	return fmt.Sprintf("zero-scale-oauth-%s", strings.ToLower(strings.TrimSpace(setupName)))
}

// OAuthProxyServiceName returns the shared oauth2-proxy Service/Deployment name.
func OAuthProxyServiceName(setupName string) string {
	return fmt.Sprintf("oauth2-proxy-%s", strings.ToLower(strings.TrimSpace(setupName)))
}

// readLine reads a line from stdin.
func readLine(prompt string) (string, error) {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// readSecretLine reads a sensitive value from stdin with terminal echo disabled when possible.
func readSecretLine(prompt string) (string, error) {
	fmt.Print(prompt)
	// Attempt to disable echo on /dev/tty
	sttyOff := exec.Command("stty", "-F", "/dev/tty", "-echo")
	echoDisabled := sttyOff.Run() == nil
	if echoDisabled {
		defer func() {
			_ = exec.Command("stty", "-F", "/dev/tty", "echo").Run()
			fmt.Println()
		}()
	}

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// verifyOAuthClientWithProvider checks OIDC discovery and probes /login/oauth/access_token
// to verify that OAUTH_CLIENT_ID and OAUTH_CLIENT_SECRET are valid on Gitea.
// If GITEA_TOKEN is available, it also queries /api/v1/user/applications/oauth2 to verify exact redirect_uris.
func verifyOAuthClientWithProvider(oauthURL, setupName, clientID, clientSecret string, requiredCallbacks []string) (bool, []string, error) {
	httpClient := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}

	// 1. Check OIDC discovery endpoint
	discURL := strings.TrimRight(oauthURL, "/") + "/.well-known/openid-configuration"
	resp, err := httpClient.Get(discURL)
	if err != nil {
		return false, nil, fmt.Errorf("failed to reach OIDC discovery endpoint %s: %w", discURL, err)
	}
	discBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Printf("    [debug] GET %s -> HTTP %d\n    [debug] Response body: %s\n", discURL, resp.StatusCode, strings.TrimSpace(string(discBody)))
		return false, nil, fmt.Errorf("OIDC discovery endpoint %s returned HTTP %d", discURL, resp.StatusCode)
	}

	// 2. Probe token endpoint with form parameters (client_id + client_secret).
	// In Gitea's AccessTokenOAuth:
	//   - Bad client_id     -> {"error":"invalid_client","error_description":"cannot load client with client id"}
	//   - Bad client_secret -> {"error":"unauthorized_client","error_description":"invalid client secret"}
	//   - Valid client_id + client_secret with dummy code -> progresses past ValidateClientSecret to code check
	//     ({"error":"invalid_grant",...} or {"error":"unauthorized_client","error_description":"unexpected redirect URI"})
	tokenURL := strings.TrimRight(oauthURL, "/") + "/login/oauth/access_token"
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("grant_type", "authorization_code")
	form.Set("code", "zs_client_probe")

	tReq, _ := http.NewRequest("POST", tokenURL, strings.NewReader(form.Encode()))
	tReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	tReq.Header.Set("Accept", "application/json")
	tResp, err := httpClient.Do(tReq)
	if err != nil {
		return false, nil, fmt.Errorf("failed to contact token endpoint %s: %w", tokenURL, err)
	}
	body, _ := io.ReadAll(tResp.Body)
	_ = tResp.Body.Close()

	var errRes struct {
		Error     string `json:"error"`
		ErrorDesc string `json:"error_description"`
	}
	_ = json.Unmarshal(body, &errRes)
	descLower := strings.ToLower(errRes.ErrorDesc)
	if errRes.Error == "invalid_client" || strings.Contains(descLower, "client secret") || strings.Contains(descLower, "client id") {
		fmt.Printf("    [debug] POST %s -> HTTP %d\n    [debug] Response body: %s\n", tokenURL, tResp.StatusCode, strings.TrimSpace(string(body)))
		return false, nil, fmt.Errorf("provider %s rejected OAUTH_CLIENT_ID / OAUTH_CLIENT_SECRET (%s: %s)", oauthURL, errRes.Error, errRes.ErrorDesc)
	}
	fmt.Printf("    [debug] POST %s -> HTTP %d (client_id & client_secret accepted; probe response: %s)\n", tokenURL, tResp.StatusCode, strings.TrimSpace(string(body)))

	// 3. If GITEA_TOKEN is available, query Gitea's OAuth2 Applications API to verify redirect_uris directly
	giteaToken := strings.TrimSpace(os.Getenv("GITEA_TOKEN"))
	if giteaToken != "" {
		apiURL := strings.TrimRight(oauthURL, "/") + "/api/v1/user/applications/oauth2"
		aReq, _ := http.NewRequest("GET", apiURL, nil)
		aReq.Header.Set("Authorization", "token "+giteaToken)
		if aResp, err := httpClient.Do(aReq); err == nil {
			defer aResp.Body.Close()
			if aResp.StatusCode == http.StatusOK {
				var apps []struct {
					ID           int64    `json:"id"`
					Name         string   `json:"name"`
					ClientID     string   `json:"client_id"`
					RedirectURIs []string `json:"redirect_uris"`
				}
				if json.NewDecoder(aResp.Body).Decode(&apps) == nil {
					for _, app := range apps {
						if app.ClientID == clientID || strings.EqualFold(app.Name, setupName) {
							return true, app.RedirectURIs, nil
						}
					}
				}
			}
		}
	}

	return false, nil, nil
}

// collectAllSetupCallbacks returns all required https://<host>/oauth2/callback URLs across
// existing apps using this setupName and the newly applied hosts.
func collectAllSetupCallbacks(setupName string, existingCM map[string]string, newHosts []string) []string {
	cbSet := make(map[string]bool)
	if existingCM != nil && existingCM["redirect_uris"] != "" {
		for _, u := range strings.Split(existingCM["redirect_uris"], ",") {
			u = strings.TrimSpace(u)
			if u != "" {
				cbSet[u] = true
			}
		}
	}
	for _, h := range newHosts {
		h = strings.TrimSpace(h)
		if h != "" {
			cbSet[fmt.Sprintf("https://%s/oauth2/callback", h)] = true
		}
	}
	var res []string
	for u := range cbSet {
		res = append(res, u)
	}
	sort.Strings(res)
	return res
}

// ReconcileOAuthSetup provisions or updates a shared oauth-setup profile (Secret, ConfigMap,
// shared oauth2-proxy Deployment & Service) and validates credentials and Redirect URIs.
func ReconcileOAuthSetup(setupName, oauthURL, oauthGroup string, appHosts []string, namespace string) error {
	setupName = strings.ToLower(strings.TrimSpace(setupName))
	if setupName == "" {
		return fmt.Errorf("oauth_setup name cannot be empty")
	}

	cmName := OAuthSetupConfigMapName(setupName)
	secName := OAuthSetupSecretName(setupName)

	existingCM, _ := GetConfigMapData(cmName, namespace)
	existingSec, _ := GetSecretData(secName, namespace)

	if oauthURL == "" && existingCM != nil {
		oauthURL = existingCM["oauth_url"]
	}
	if oauthGroup == "" && existingCM != nil {
		oauthGroup = existingCM["oauth_group"]
	}
	if oauthURL == "" {
		return fmt.Errorf("oauth_url is required for oauth-setup '%s' (specify oauth_url under gateway: in .conf or run 'zero-scale-ctl oauth-setup %s --url <url>')", setupName, setupName)
	}
	oauthURL = strings.TrimRight(strings.TrimSpace(oauthURL), "/")

	requiredCallbacks := collectAllSetupCallbacks(setupName, existingCM, appHosts)

	// Credential resolution order:
	// 1. Environment variables OAUTH_CLIENT_ID & OAUTH_CLIENT_SECRET
	// 2. Existing Kubernetes Secret (zero-scale-oauth-<setupName>)
	// 3. Guided interactive CLI prompt
	clientID := strings.TrimSpace(os.Getenv("OAUTH_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("OAUTH_CLIENT_SECRET"))

	if clientID != "" && clientSecret != "" {
		fmt.Printf("--> Using OAUTH_CLIENT_ID and OAUTH_CLIENT_SECRET from environment for shared oauth-setup '%s'.\n", setupName)
	} else if existingSec != nil && existingSec["OAUTH_CLIENT_ID"] != "" && existingSec["OAUTH_CLIENT_SECRET"] != "" {
		clientID = existingSec["OAUTH_CLIENT_ID"]
		clientSecret = existingSec["OAUTH_CLIENT_SECRET"]
		fmt.Printf("--> Found existing credentials in Kubernetes Secret '%s'.\n", secName)
	}

	for {
		if clientID == "" || clientSecret == "" {
			fmt.Println("")
			fmt.Println("==============================================================================")
			fmt.Printf("🔐 Shared OAuth2 Setup Required: '%s'\n", setupName)
			fmt.Println("==============================================================================")
			fmt.Printf("  1. Open Gitea Applications: %s/-/admin/applications\n", oauthURL)
			fmt.Printf("  2. Application Name:        %s\n", setupName)
			fmt.Printf("  3. Confidential Client:     Checked (Yes)\n")
			fmt.Printf("  4. Redirect URI(s) to add:\n")
			for _, cb := range requiredCallbacks {
				fmt.Printf("       %s\n", cb)
			}
			fmt.Println("------------------------------------------------------------------------------")

			var err error
			for clientID == "" {
				clientID, err = readLine("Enter OAUTH_CLIENT_ID: ")
				if err != nil {
					return fmt.Errorf("failed reading OAUTH_CLIENT_ID: %w", err)
				}
			}
			for clientSecret == "" {
				clientSecret, err = readSecretLine("Enter OAUTH_CLIENT_SECRET (hidden): ")
				if err != nil {
					return fmt.Errorf("failed reading OAUTH_CLIENT_SECRET: %w", err)
				}
			}
		}

		fmt.Printf("--> Verifying OAuth2 application '%s' against %s...\n", setupName, oauthURL)
		apiVerified, remoteRedirects, err := verifyOAuthClientWithProvider(oauthURL, setupName, clientID, clientSecret, requiredCallbacks)
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			retryAns, _ := readLine("Re-enter OAuth client credentials? [Y/n]: ")
			if strings.ToLower(strings.TrimSpace(retryAns)) == "n" {
				return fmt.Errorf("OAuth verification failed: %w", err)
			}
			clientID = ""
			clientSecret = ""
			continue
		}
		fmt.Printf("--> OAuth2 client '%s' authenticated successfully against %s/login/oauth/introspect.\n", clientID, oauthURL)

		if apiVerified {
			for {
				remoteSet := make(map[string]bool)
				for _, r := range remoteRedirects {
					remoteSet[strings.TrimSpace(r)] = true
				}
				var missing []string
				for _, reqCB := range requiredCallbacks {
					if !remoteSet[reqCB] {
						missing = append(missing, reqCB)
					}
				}
				if len(missing) == 0 {
					fmt.Printf("--> Verified all required Redirect URIs on Gitea app '%s': %v\n", setupName, requiredCallbacks)
					break
				}
				fmt.Printf("⚠️  Gitea OAuth application '%s' is missing required Redirect URI(s):\n", setupName)
				for _, m := range missing {
					fmt.Printf("     - %s\n", m)
				}
				fmt.Printf("   Update the Redirect URIs at %s/-/admin/applications so it includes:\n", oauthURL)
				for _, cb := range requiredCallbacks {
					fmt.Printf("     %s\n", cb)
				}
				ans, _ := readLine("Press [Enter] after updating the Redirect URI in Gitea to re-check (or 's' to skip): ")
				if strings.ToLower(strings.TrimSpace(ans)) == "s" {
					break
				}
				_, remoteRedirects, _ = verifyOAuthClientWithProvider(oauthURL, setupName, clientID, clientSecret, requiredCallbacks)
			}
		} else {
			prevSet := make(map[string]bool)
			if existingCM != nil {
				for _, u := range strings.Split(existingCM["redirect_uris"], ",") {
					if strings.TrimSpace(u) != "" {
						prevSet[strings.TrimSpace(u)] = true
					}
				}
			}
			var newlyAdded []string
			for _, reqCB := range requiredCallbacks {
				if !prevSet[reqCB] {
					newlyAdded = append(newlyAdded, reqCB)
				}
			}
			if len(newlyAdded) > 0 {
				fmt.Printf("--> Required Redirect URI(s) for '%s' (%s/-/admin/applications):\n", setupName, oauthURL)
				for _, cb := range requiredCallbacks {
					fmt.Printf("      • %s\n", cb)
				}
			}
		}
		break
	}

	cookieSecret := ""
	if existingSec != nil && len(existingSec["OAUTH_COOKIE_SECRET"]) == 32 {
		cookieSecret = existingSec["OAUTH_COOKIE_SECRET"]
	} else {
		// 32 ASCII bytes is directly accepted by oauth2-proxy as a 32-byte AES cipher key
		cookieSecret = GeneratePassword(32)
	}

	labels := map[string]string{
		"zero-scale-oauth":       "true",
		"zero-scale-oauth-setup": setupName,
	}

	if err := CreateOrUpdateSecret(secName, namespace, map[string]string{
		"OAUTH_CLIENT_ID":     clientID,
		"OAUTH_CLIENT_SECRET": clientSecret,
		"OAUTH_COOKIE_SECRET": cookieSecret,
	}, labels); err != nil {
		return fmt.Errorf("failed to save OAuth secret %s: %w", secName, err)
	}

	if err := CreateOrUpdateConfigMap(cmName, namespace, map[string]string{
		"setup_name":    setupName,
		"oauth_url":     oauthURL,
		"oauth_group":   oauthGroup,
		"redirect_uris": strings.Join(requiredCallbacks, ","),
	}, labels); err != nil {
		return fmt.Errorf("failed to save OAuth configmap %s: %w", cmName, err)
	}

	// Deploy or update shared oauth2-proxy-<setupName> Deployment & Service
	if err := deploySharedOAuth2Proxy(setupName, oauthURL, oauthGroup, requiredCallbacks, namespace); err != nil {
		return fmt.Errorf("failed to deploy shared oauth2-proxy for '%s': %w", setupName, err)
	}

	// If appHosts were provided, apply the /oauth2 Ingress for those hosts
	if len(appHosts) > 0 {
		if err := ApplyOAuth2CallbackIngress(setupName, namespace, appHosts); err != nil {
			return fmt.Errorf("failed to apply /oauth2 ingress: %w", err)
		}
	}

	return nil
}

// deploySharedOAuth2Proxy deploys the shared oauth2-proxy Deployment and Service for setupName.
func deploySharedOAuth2Proxy(setupName, oauthURL, oauthGroup string, requiredCallbacks []string, namespace string) error {
	svcName := OAuthProxyServiceName(setupName)
	secName := OAuthSetupSecretName(setupName)

	if !ResourceExists("service", "shared-redis", namespace) {
		fmt.Printf("⚠️  Warning: Service 'shared-redis' was not found in namespace '%s'.\n", namespace)
		fmt.Printf("--> oauth2-proxy requires shared-redis for session storage.\n")
		fmt.Printf("--> Deploy with: kubectl apply -f deploy/shared-data-services.yaml -n %s\n", namespace)
		fmt.Println("--> Documentation: https://git.fairfaxmedia.net/open-cream-cheese/zero-scale-platform#oauth2-authentication")
	}

	// Extract unique parent domains (e.g. .ffxm.cc, .fairfaxmedia.net) for --whitelist-domain and --cookie-domain
	domainSet := make(map[string]bool)
	for _, cb := range requiredCallbacks {
		if u, err := url.Parse(cb); err == nil && u.Hostname() != "" {
			parts := strings.Split(u.Hostname(), ".")
			if len(parts) >= 2 {
				parentDomain := "." + strings.Join(parts[1:], ".")
				domainSet[parentDomain] = true
			}
			domainSet[u.Hostname()] = true
		}
	}
	var whitelistArgs []string
	for d := range domainSet {
		whitelistArgs = append(whitelistArgs, fmt.Sprintf("        - --whitelist-domain=%s", d))
	}
	sort.Strings(whitelistArgs)

	// Build --allowed-group flags for Gitea OIDC
	// Note: Gitea OIDC /login/oauth/userinfo returns groups in "<org>:<Team>" format (e.g. "open-cream-cheese:Owners")
	// as well as "<org>", so for any group without ":" we include <group>, <group>:Owners, and <group>:owners.
	var groupArgs []string
	if strings.TrimSpace(oauthGroup) != "" {
		seenGroups := make(map[string]bool)
		for _, rawG := range strings.FieldsFunc(oauthGroup, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
			g := strings.TrimSpace(rawG)
			if g == "" {
				continue
			}
			candidates := []string{g}
			if !strings.Contains(g, ":") {
				candidates = append(candidates,
					g+":Owners",
					g+":owners",
					g+":Members",
					g+":members",
					g+":Admin",
					g+":admin",
				)
			}
			for _, c := range candidates {
				if !seenGroups[c] {
					seenGroups[c] = true
					groupArgs = append(groupArgs, fmt.Sprintf("        - --allowed-group=%s", c))
				}
			}
		}
	}

	extraArgs := ""
	if len(whitelistArgs) > 0 {
		extraArgs += "\n" + strings.Join(whitelistArgs, "\n")
	}
	if len(groupArgs) > 0 {
		extraArgs += "\n" + strings.Join(groupArgs, "\n")
	}

	manifest := fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
    zero-scale-oauth-setup: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %s
  template:
    metadata:
      labels:
        app: %s
        zero-scale-oauth-setup: %s
    spec:
      containers:
      - name: oauth2-proxy
        image: quay.io/oauth2-proxy/oauth2-proxy:v7.6.0
        args:
        - --provider=oidc
        - --oidc-issuer-url=%s
        - --scope=openid profile email groups
        - --oidc-groups-claim=groups
        - --oidc-email-claim=email
        - --prefer-email-to-user=false
        - --user-id-claim=preferred_username
        - --email-domain=*
        - --upstream=static://202
        - --http-address=0.0.0.0:4180
        - --reverse-proxy=true
        - --set-xauthrequest=true
        - --pass-user-headers=true
        - --skip-provider-button=true
        - --session-store-type=redis
        - --redis-connection-url=redis://shared-redis.%s.svc.cluster.local:6379
        - --cookie-name=_zs_oauth_%s
        - --cookie-secure=true
        - --cookie-samesite=lax%s
        env:
        - name: OAUTH2_PROXY_CLIENT_ID
          valueFrom:
            secretKeyRef:
              name: %s
              key: OAUTH_CLIENT_ID
        - name: OAUTH2_PROXY_CLIENT_SECRET
          valueFrom:
            secretKeyRef:
              name: %s
              key: OAUTH_CLIENT_SECRET
        - name: OAUTH2_PROXY_COOKIE_SECRET
          valueFrom:
            secretKeyRef:
              name: %s
              key: OAUTH_COOKIE_SECRET
        ports:
        - containerPort: 4180
          name: http
        resources:
          requests:
            cpu: 10m
            memory: 16Mi
          limits:
            cpu: 100m
            memory: 64Mi
---
apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  labels:
    app: %s
    zero-scale-oauth-setup: %s
spec:
  selector:
    app: %s
  ports:
  - name: http
    port: 4180
    targetPort: 4180
    protocol: TCP
`, svcName, namespace, svcName, setupName, svcName, svcName, setupName,
		oauthURL, namespace, setupName, extraArgs,
		secName, secName, secName,
		svcName, namespace, svcName, setupName, svcName)

	return ApplyYAML(manifest, namespace)
}

// ApplyOAuth2CallbackIngress creates/updates an Ingress routing /oauth2 on the app's hosts
// directly to the shared oauth2-proxy-<setupName> Service.
func ApplyOAuth2CallbackIngress(setupName, namespace string, hosts []string) error {
	if len(hosts) == 0 {
		return nil
	}
	svcName := OAuthProxyServiceName(setupName)
	primaryShort := strings.Split(hosts[0], ".")[0]
	ingName := fmt.Sprintf("zero-scale-%s-oauth2", primaryShort)

	var b strings.Builder
	b.WriteString(fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: %s
  namespace: %s
  labels:
    app: zero-scale-gateway
    zero-scale-oauth-setup: %s
  annotations:
    nginx.ingress.kubernetes.io/ssl-redirect: "false"
    nginx.ingress.kubernetes.io/proxy-buffer-size: "16k"
spec:
`, ingName, namespace, setupName))
	if cls := resolveIngressClass(); cls != "" {
		b.WriteString(fmt.Sprintf("  ingressClassName: %s\n", cls))
	}
	b.WriteString("  rules:\n")

	for _, host := range hosts {
		b.WriteString(fmt.Sprintf(`  - host: %s
    http:
      paths:
      - path: /oauth2
        pathType: Prefix
        backend:
          service:
            name: %s
            port:
              number: 4180
`, host, svcName))
	}
	return ApplyYAML(b.String(), namespace)
}
