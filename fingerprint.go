package restys

import (
	"fmt"
	"strings"
)

type WebGL struct {
	Render    string `json:"render"`
	Vendor    string `json:"vender"`
	ToDataURL int    `json:"todataurl"`
}
type Fingerprint struct {
	ClientHint struct {
		Architecture string `json:"architecture"`
		Bitness      string `json:"bitness"`
		Brands       []struct {
			Brand   string `json:"brand"`
			Version string `json:"version"`
		} `json:"brands"`
		FullVersionList []struct {
			Brand   string `json:"brand"`
			Version string `json:"version"`
		} `json:"fullVersionList"`
		Mobile          bool   `json:"mobile"`
		Platform        string `json:"platform"`
		PlatformVersion string `json:"platformVersion"`
		UaFullVersion   string `json:"uaFullVersion"`
	} `json:"clientHint"`
	WebGL     WebGL  `json:"webgl"`
	UserAgent string `json:"navigator.userAgent"`
	Platform  string `json:"navigator.platform"`
	Vendor    string `json:"navigator.vendor"`
	WebRtc    struct {
		Public  string `json:"public"`
		Private string `json:"private"`
	} `json:"webrtc"`
}

// GenerateSecCHUA 生成 sec-ch-ua 字段
func (ch *Fingerprint) GenerateSecCHUA() string {
	var uaBrands []string
	for _, brand := range ch.ClientHint.Brands {
		uaBrands = append(uaBrands, fmt.Sprintf(`"%s";v="%s"`, brand.Brand, brand.Version))
	}
	return strings.Join(uaBrands, ", ")
}

// GenerateSecCHUAMobile 生成 sec-ch-ua-mobile 字段
func (ch *Fingerprint) GenerateSecCHUAMobile() string {
	if ch.ClientHint.Mobile {
		return "?1"
	}
	return "?0"
}

// GenerateSecCHUAPlatform 生成 sec-ch-ua-platform 字段
func (ch *Fingerprint) GenerateSecCHUAPlatform() string {
	return fmt.Sprintf(`"%s"`, ch.ClientHint.Platform)
}
