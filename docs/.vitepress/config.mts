import { defineConfig } from "vitepress";
import llmstxt from "vitepress-plugin-llms";

const env = (globalThis as { process?: { env?: Record<string, string | undefined> } }).process?.env ?? {};
const base = env.VITEPRESS_BASE ?? "/p2pstream/";
const siteUrl = env.VITEPRESS_SITE_URL ?? `https://kirari04.github.io${base}`;

export default defineConfig({
  title: "p2pstream",
  description: "Self-hosted reverse proxy and remote agent documentation.",
  base,
  cleanUrls: true,
  lastUpdated: true,
  sitemap: {
    hostname: siteUrl
  },
  vite: {
    plugins: [
      llmstxt({
        title: "p2pstream",
        description: "Self-hosted reverse proxy and remote agent documentation."
      })
    ]
  },
  head: [
    ["meta", { name: "theme-color", content: "#111827" }],
    ["meta", { property: "og:type", content: "website" }],
    ["meta", { property: "og:title", content: "p2pstream documentation" }],
    ["meta", { property: "og:description", content: "Self-hosting operations guide for p2pstream." }]
  ],
  themeConfig: {
    search: {
      provider: "local"
    },
    outline: {
      level: [2, 3],
      label: "On this page"
    },
    nav: [
      { text: "Start", link: "/getting-started/quickstart" },
      { text: "Publish", link: "/guides/publish-a-service" },
      { text: "TLS/Security", link: "/concepts/tls" },
      { text: "Operate", link: "/operations/troubleshooting" },
      { text: "Reference", link: "/reference/configuration" },
      { text: "GitHub", link: "https://github.com/Kirari04/p2pstream" }
    ],
    sidebar: [
      {
        text: "Start Here",
        items: [
          { text: "Docker Compose Quickstart", link: "/getting-started/quickstart" },
          { text: "First Login", link: "/getting-started/first-login" },
          { text: "Docker Compose Details", link: "/getting-started/docker-compose" },
          { text: "Release Binary", link: "/getting-started/binary" }
        ]
      },
      {
        text: "Publish Apps",
        items: [
          { text: "Publish a Service", link: "/guides/publish-a-service" },
          { text: "Expose a Home Lab App", link: "/guides/expose-a-home-lab-app" },
          { text: "Build an Agent Pool", link: "/guides/agent-pool" },
          { text: "Redirects and Static Responses", link: "/guides/redirects-and-static-responses" },
          { text: "Architecture", link: "/concepts/architecture" },
          { text: "Listeners", link: "/concepts/listeners" },
          { text: "Routing", link: "/concepts/routing" },
          { text: "Backends", link: "/concepts/backends" },
          { text: "Agents", link: "/concepts/agents" }
        ]
      },
      {
        text: "TLS and Security",
        items: [
          { text: "TLS", link: "/concepts/tls" },
          { text: "ACME HTTP/TLS-ALPN", link: "/guides/acme-http-tls-alpn" },
          { text: "ACME Cloudflare DNS", link: "/guides/acme-cloudflare-dns" },
          { text: "Security Hardening", link: "/operations/security-hardening" },
          { text: "Management TLS Reference", link: "/reference/management-tls" },
          { text: "Environments", link: "/reference/environments" },
          { text: "Public TLS and ACME", link: "/reference/public-tls-acme" }
        ]
      },
      {
        text: "Traffic Controls",
        items: [
          { text: "Rate Limit a Route", link: "/guides/rate-limit-a-route" },
          { text: "Shape Bandwidth", link: "/guides/shape-bandwidth" },
          { text: "Limits and Shaping", link: "/concepts/limits-and-shaping" },
          { text: "WAF", link: "/concepts/waf" },
          { text: "Public Asset Cache", link: "/concepts/cache" },
          { text: "CEL Policy Matching", link: "/reference/cel" },
          { text: "Rate Limits Reference", link: "/reference/rate-limits" },
          { text: "Traffic Shaping Reference", link: "/reference/traffic-shaping" },
          { text: "WAF Reference", link: "/reference/waf" },
          { text: "Response Templates", link: "/reference/response-templates" },
          { text: "Cache Reference", link: "/reference/cache" }
        ]
      },
      {
        text: "Operate",
        items: [
          { text: "Troubleshooting", link: "/operations/troubleshooting" },
          { text: "Trace Live Traffic", link: "/guides/trace-live-traffic" },
          { text: "Observability", link: "/concepts/observability" },
          { text: "Backup and Restore", link: "/operations/backup-restore" },
          { text: "Upgrades", link: "/operations/upgrades" },
          { text: "Systemd", link: "/operations/systemd" },
          { text: "Screenshots", link: "/reference/screenshots" }
        ]
      },
      {
        text: "Reference",
        items: [
          { text: "Configuration", link: "/reference/configuration" },
          { text: "CLI", link: "/reference/cli" },
          { text: "Docker", link: "/reference/docker" },
          { text: "Ports", link: "/reference/ports" },
          { text: "LLM-Ready Docs", link: "/reference/llms" },
          { text: "License", link: "/reference/license" },
          { text: "Database", link: "/reference/database" },
          { text: "Routing Rules", link: "/reference/routing-rules" },
          { text: "CEL Policy Matching", link: "/reference/cel" },
          { text: "Management TLS", link: "/reference/management-tls" },
          { text: "Environments", link: "/reference/environments" },
          { text: "Public TLS and ACME", link: "/reference/public-tls-acme" },
          { text: "Response Templates", link: "/reference/response-templates" },
          { text: "Rate Limits", link: "/reference/rate-limits" },
          { text: "Traffic Shaping", link: "/reference/traffic-shaping" },
          { text: "WAF", link: "/reference/waf" },
          { text: "Cache", link: "/reference/cache" }
        ]
      }
    ],
    socialLinks: [
      { icon: "github", link: "https://github.com/Kirari04/p2pstream" }
    ],
    footer: {
      message: "Operations documentation for self-hosted p2pstream deployments."
    }
  }
});
