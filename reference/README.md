# Reference

- `sample-verify-page.html`: the working sample verify page (vanilla JS + Web Crypto) built during discovery. Task P1-6 ports it into `web/verify/` and restyles it with `web/brand.css`. Its `__BUNDLE__` placeholder was where sample envelopes were injected.
- `verify-reference.test.mjs`: the executable definition of verification. Runs with `node --test` and no packages. P1-6 moves these functions into `web/verify/verify.js`; P1-4 must produce identical results in Go.
