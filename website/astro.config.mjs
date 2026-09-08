import { defineConfig } from "astro/config";
import starlight from "@astrojs/starlight";

export default defineConfig({
  output: "static",
  site: process.env.PUBLIC_SITE_URL,
  integrations: [
    starlight({
      title: "Argo",
      description: "Documentation for the Argo traffic-aware download manager.",
      logo: { src: "./public/branding/logo/argo-logo-horizontal.svg", replacesTitle: true },
      social: [
        { icon: "github", label: "GitHub", href: "https://github.com/kristyancarvalho/argo" },
      ],
      editLink: { baseUrl: "https://github.com/kristyancarvalho/argo/edit/dev/website/" },
      customCss: ["./src/styles/starlight.css"],
      sidebar: [
        { label: "Introduction", link: "/docs/" },
        {
          label: "Getting Started",
          items: [{ label: "Installation and first download", link: "/docs/getting-started/" }],
        },
        {
          label: "Usage",
          items: [{ label: "Download lifecycle and terminal tools", link: "/docs/downloads/" }],
        },
        {
          label: "Configuration",
          items: [{ label: "Config file, profiles, and XDG paths", link: "/docs/configuration/" }],
        },
        {
          label: "Networking",
          items: [{ label: "Awareness and traffic policies", link: "/docs/networking/" }],
        },
        {
          label: "Architecture",
          items: [{ label: "Components and security boundaries", link: "/docs/architecture/" }],
        },
        {
          label: "Development",
          items: [{ label: "Building, testing, and workflow", link: "/docs/development/" }],
        },
        { label: "Troubleshooting", link: "/docs/troubleshooting/" },
      ],
    }),
  ],
});
