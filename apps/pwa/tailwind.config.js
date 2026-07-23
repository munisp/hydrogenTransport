/** @type {import('tailwindcss').Config} */
// Low-saturation warm palette (SPEC §3.7): stone base, amber + teal accents.
export default {
  content: ["./index.html", "./src/**/*.{ts,tsx}"],
  theme: {
    extend: {
      colors: {
        surface: {
          DEFAULT: "#fafaf9", // stone-50
          raised: "#ffffff",
          sunken: "#f5f5f4", // stone-100
        },
        accent: {
          DEFAULT: "#b45309", // amber-700 — hydrogen warmth
          soft: "#fef3c7", // amber-100
          muted: "#92400e", // amber-800
        },
        teal: {
          accent: "#0f766e", // teal-700 — clean energy
          soft: "#ccfbf1", // teal-100
        },
      },
      fontFamily: {
        sans: [
          "ui-sans-serif",
          "system-ui",
          "-apple-system",
          "Segoe UI",
          "Roboto",
          "Helvetica Neue",
          "Arial",
          "sans-serif",
        ],
      },
      boxShadow: {
        card: "0 1px 2px 0 rgb(28 25 23 / 0.05)",
      },
    },
  },
  plugins: [],
};
