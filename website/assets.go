package website

import "embed"

//go:embed index.html style.css app.html app.js app.css
var Files embed.FS
