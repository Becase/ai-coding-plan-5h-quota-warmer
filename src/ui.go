package main

import _ "embed"

//go:embed web/index.html
var indexHTML []byte

//go:embed web/third-party-notices.txt
var thirdPartyNotices []byte
