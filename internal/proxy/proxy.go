package proxy

import (
	"fmt"
	"os/exec"
	"strings"
)

// ProxyHint describes a detectable reverse proxy or tunnel.
type ProxyHint struct {
	Name     string
	Detect   func() bool
	Guidance func(path string, port int) string
}

var hints = []ProxyHint{
	{
		Name:   "nginx",
		Detect: makeDetect([]string{"nginx"}, []string{"nginx"}),
		Guidance: func(path string, port int) string {
			return fmt.Sprintf(""+
				"    Add to your nginx config:\n"+
				"      location = %s {\n"+
				"          proxy_pass http://127.0.0.1:%d;\n"+
				"          proxy_set_header Host $host;\n"+
				"          proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;\n"+
				"      }\n"+
				"    Then: sudo nginx -t && sudo systemctl reload nginx",
				path, port)
		},
	},
	{
		Name:   "caddy",
		Detect: makeDetect([]string{"caddy"}, []string{"caddy"}),
		Guidance: func(path string, port int) string {
			return fmt.Sprintf(""+
				"    Add to your Caddyfile:\n"+
				"      reverse_proxy %s localhost:%d\n"+
				"    Then: caddy reload",
				path, port)
		},
	},
	{
		Name:   "traefik",
		Detect: makeDetect([]string{"traefik"}, []string{"traefik"}),
		Guidance: func(path string, port int) string {
			return fmt.Sprintf(""+
				"    Add a Traefik dynamic config:\n"+
				"      http:\n"+
				"        routers:\n"+
				"          pylon:\n"+
				"            rule: \"PathPrefix(`%s`)\"\n"+
				"            service: pylon\n"+
				"        services:\n"+
				"          pylon:\n"+
				"            loadBalancer:\n"+
				"              servers:\n"+
				"                - url: \"http://127.0.0.1:%d\"",
				path, port)
		},
	},
	{
		Name:   "haproxy",
		Detect: makeDetect([]string{"haproxy"}, []string{"haproxy"}),
		Guidance: func(path string, port int) string {
			return fmt.Sprintf(""+
				"    Add to your haproxy.cfg:\n"+
				"      frontend http\n"+
				"          acl pylon_path path_beg %s\n"+
				"          use_backend pylon if pylon_path\n"+
				"      backend pylon\n"+
				"          server pylon 127.0.0.1:%d\n"+
				"    Then: sudo systemctl reload haproxy",
				path, port)
		},
	},
	{
		Name:   "apache",
		Detect: makeDetect([]string{"httpd", "apache2"}, []string{"httpd", "apache2"}),
		Guidance: func(path string, port int) string {
			return fmt.Sprintf(""+
				"    Enable mod_proxy and add to your Apache config:\n"+
				"      ProxyPass \"%s\" \"http://127.0.0.1:%d%s\"\n"+
				"      ProxyPassReverse \"%s\" \"http://127.0.0.1:%d%s\"\n"+
				"    Then: sudo systemctl reload apache2",
				path, port, path, path, port, path)
		},
	},
	{
		Name:   "cloudflared",
		Detect: makeDetect([]string{"cloudflared"}, []string{"cloudflared"}),
		Guidance: func(path string, port int) string {
			return fmt.Sprintf(""+
				"    Add to your cloudflared config (~/.cloudflared/config.yml):\n"+
				"      ingress:\n"+
				"        - hostname: <your-hostname>\n"+
				"          path: %s\n"+
				"          service: http://localhost:%d\n"+
				"        - service: http_status:404\n"+
				"    Then: cloudflared tunnel run",
				path, port)
		},
	},
	{
		Name:   "ngrok",
		Detect: makeDetect([]string{"ngrok"}, []string{"ngrok"}),
		Guidance: func(_ string, port int) string {
			return fmt.Sprintf(""+
				"    Expose pylon via ngrok:\n"+
				"      ngrok http %d\n"+
				"    Then update your public_url in ~/.pylon/config.yaml to the ngrok URL.",
				port)
		},
	},
	{
		Name:   "tailscale",
		Detect: makeDetect([]string{"tailscaled"}, []string{"tailscale"}),
		Guidance: func(_ string, port int) string {
			return fmt.Sprintf(""+
				"    Expose pylon via Tailscale Funnel:\n"+
				"      tailscale funnel --bg %d\n"+
				"    Then update your public_url in ~/.pylon/config.yaml to your Tailscale hostname.",
				port)
		},
	},
}

// PrintHints detects proxies/tunnels and prints guidance for routing
// traffic to the given webhook path and port. Returns true if any
// proxy was detected.
func PrintHints(path string, port int) bool {
	var detected []ProxyHint
	for _, h := range hints {
		if h.Detect() {
			detected = append(detected, h)
		}
	}

	if len(detected) == 0 {
		fmt.Printf("  No reverse proxy detected. To receive webhooks, route traffic to localhost:%d.\n", port)
		fmt.Println("  Options: nginx, caddy, cloudflared, ngrok, or tailscale funnel.")
		return false
	}

	if len(detected) == 1 {
		h := detected[0]
		fmt.Printf("  Detected: %s\n", h.Name)
		fmt.Println(h.Guidance(path, port))
		return true
	}

	names := make([]string, len(detected))
	for i, h := range detected {
		names[i] = h.Name
	}
	fmt.Printf("  Detected: %s\n\n", strings.Join(names, ", "))
	for _, h := range detected {
		fmt.Printf("  %s:\n", h.Name)
		fmt.Println(h.Guidance(path, port))
		fmt.Println()
	}
	return true
}

func makeDetect(processNames, binaryNames []string) func() bool {
	return func() bool {
		return detectProcess(processNames...) || detectBinary(binaryNames...)
	}
}

func detectProcess(names ...string) bool {
	for _, name := range names {
		if err := exec.Command("pgrep", "-x", name).Run(); err == nil {
			return true
		}
	}
	return false
}

func detectBinary(names ...string) bool {
	for _, name := range names {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}
