# Website review — 18 November 2026

## Public deployment

Reviewed https://relay-dun-ten-19.vercel.app in Chromium at 1440, 1024, 768, 390, and 320px. Inspected full-page desktop/mobile screenshots and repeated the viewport checks after production deployment.

Fixed and deployed:

- The code example expanded its CSS grid track and made 390px and 320px screens scroll sideways. Grid tracks now shrink correctly and the code block scrolls internally.
- A large decorative star covered the bottom call-to-action text. It is now a subdued SVG behind content, sized appropriately on mobile.
- Navigation disappeared below the desktop breakpoint. A native, keyboard-operable mobile menu now exposes Features, How it works, Pricing, and Documentation.
- Light green headings and muted text failed contrast checks. Updated the palette while retaining the existing green theme, including the local dashboard.
- Replaced unsupported decorative arrow/star glyphs with SVGs, added a skip link and visible focus styles, and respected reduced-motion preferences.
- Updated the sample request to the actual project-key/endpoint API.

## Checks and outcomes

| Check | Outcome |
| --- | --- |
| Production root and CSS | Loaded successfully |
| Page-wide horizontal overflow at all five widths | None after fixes |
| Live JavaScript exceptions / failed resource requests | None observed |
| Desktop section navigation | Passed |
| Mobile menu section navigation | Passed |
| Keyboard skip link | Passed |
| Back-to-top link | Passed |
| 200% layout scaling | Passed |
| Four distinct GitHub documentation/repository link targets | HTTP success |
| Automated WCAG 2 A/AA and WCAG 2.1 AA checks on production landing page | No axe violations detected |
| Go tests and application build | Passed |

The code example intentionally scrolls inside its own block on narrow screens; this is not page-wide overflow. The decorative SVG is clipped inside its section.

## Local application checks

The SaaS application was exercised locally using a disposable SQLite database and a local webhook receiver. Signup, project creation, endpoint registration, one-time API key generation, test-event submission, signed webhook delivery, delivery attempts, logout, and subsequent login passed.

The five dashboard views (Overview, Deliveries, Endpoints, API keys, Usage) were checked at 1440, 390, and 320px. No page-wide overflow or automated axe violations remained after contrast corrections. The login page also passed its automated accessibility check.

## Public functionality still unavailable

The public URL remains a static landing page. `/app` returns HTTP 404. Public signup, event submission, and the dashboard cannot work until the backend is hosted. Database/hosting setup was paused at the user's request; this review did not restart it or create a paid service.

The static build deliberately points calls to action at working self-hosting documentation and states that the hosted dashboard is unavailable. `scripts/build-static-site.mjs` reproduces this deployment without publishing broken `/app` links. The Go application's embedded landing page retains its working `/app` links for when the backend is deployed.

## Limits of this review

Tests used Chromium viewport emulation, not physical devices or Safari/Firefox. Live HTTPS resources were fetched through the environment's configured proxy with certificate verification enabled and rendered in an isolated browser context because Chromium's direct certificate trust failed in this managed environment. This does not establish performance on a user's network. Automated accessibility scans do not establish full WCAG conformance or substitute for screen-reader testing.
