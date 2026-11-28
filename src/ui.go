package main

import (
	"bytes"
	_ "embed"
)

//go:embed web/index.html
var indexTemplate []byte

var indexHTML = bytes.ReplaceAll(indexTemplate, []byte("{{APP_VERSION}}"), []byte(serviceVersion))

//go:embed web/third-party-notices.txt
var thirdPartyNotices []byte
