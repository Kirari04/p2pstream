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
    nav: [
      { text: "Guide", link: "/getting-started/quickstart" },
      { text: "Concepts", link: "/concepts/architecture" },
      { text: "Operations", link: "/operations/security-hardening" },
      { text: "Reference", link: "/reference/configuration" },
      { text: "GitHub", link: "https://github.com/Kirari04/p2pstream" }
    ],
    sidebar: [
      {
        text: "Get Started",
        items: [
          { text: "Docker Compose Quickstart", link: "/getting-started/quickstart" },
          { text: "Docker Compose Details", link: "/getting-started/docker-compose" },
          { text: "Release Binary (advanced)", link: "/getting-started/binary" },
          { text: "First Login", link: "/getting-started/first-login" }
        ]
      },
      {
        text: "Core Concepts",
        items: [
          { text: "Architecture", link: "/concepts/architecture" },
          { text: "Listeners", link: "/concepts/listeners" },
          { text: "Routing", link: "/concepts/routing" },
          { text: "Backends", link: "/concepts/backends" },
          { text: "Agents", link: "/concepts/agents" },
          { text: "TLS", link: "/concepts/tls" },
          { text: "Limits and Shaping", link: "/concepts/limits-and-shaping" },
          { text: "Observability", link: "/concepts/observability" }
        ]
      },
      {
        text: "Guides",
        items: [
          { text: "Publish a Service", link: "/guides/publish-a-service" },
          { text: "Expose a Home Lab App", link: "/guides/expose-a-home-lab-app" },
          { text: "Agent Pool", link: "/guides/agent-pool" },
          { text: "Redirects and Static Responses", link: "/guides/redirects-and-static-responses" },
          { text: "ACME HTTP/TLS-ALPN", link: "/guides/acme-http-tls-alpn" },
          { text: "ACME Cloudflare DNS", link: "/guides/acme-cloudflare-dns" },
          { text: "Rate Limit a Route", link: "/guides/rate-limit-a-route" },
          { text: "Shape Bandwidth", link: "/guides/shape-bandwidth" },
          { text: "Trace Live Traffic", link: "/guides/trace-live-traffic" }
        ]
      },
      {
        text: "Operations",
        items: [
          { text: "Security Hardening", link: "/operations/security-hardening" },
          { text: "Backup and Restore", link: "/operations/backup-restore" },
          { text: "Upgrades", link: "/operations/upgrades" },
          { text: "Systemd (advanced)", link: "/operations/systemd" },
          { text: "Troubleshooting", link: "/operations/troubleshooting" }
        ]
      },
      {
        text: "Reference",
        items: [
          { text: "Configuration", link: "/reference/configuration" },
          { text: "CLI", link: "/reference/cli" },
          { text: "Ports", link: "/reference/ports" },
          { text: "Database", link: "/reference/database" },
          { text: "Docker", link: "/reference/docker" },
          { text: "Management TLS", link: "/reference/management-tls" },
          { text: "Public TLS and ACME", link: "/reference/public-tls-acme" },
          { text: "Routing Rules", link: "/reference/routing-rules" },
          { text: "Rate Limits", link: "/reference/rate-limits" },
          { text: "Traffic Shaping", link: "/reference/traffic-shaping" }
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
