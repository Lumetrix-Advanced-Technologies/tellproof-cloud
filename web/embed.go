// Package web embeds the app's templates and stylesheets and the brand tokens,
// so tellproof-server serves exactly the files it was built with. The static
// verify page (verify/) is deployed on its own and is not embedded.
package web

import "embed"

// Files holds brand.css and app/ (templates/*.html, app.css).
//
//go:embed brand.css app
var Files embed.FS
