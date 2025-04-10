package restys

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"
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

func ParseFingerprint(str string) (fp *Fingerprint) {
	json.Unmarshal([]byte(str), &fp)
	return
}

func GenerateRandomFingerprint(browserType int) *Fingerprint {
	bigVersion := "130"
	rand.Seed(time.Now().UnixNano())
	fp := &Fingerprint{}
	rand1 := rand.Intn(900) + 100
	rand2 := rand.Intn(98) + 1
	// ClientHint
	fp.ClientHint.Architecture = "x86"
	fp.ClientHint.Bitness = "64"
	fp.ClientHint.Brands = []struct {
		Brand   string `json:"brand"`
		Version string `json:"version"`
	}{
		//{"Microsoft Edge", "119"},
		{"Chromium", bigVersion},
		{"Not=A?Brand", "24"},
	}
	fp.ClientHint.FullVersionList = []struct {
		Brand   string `json:"brand"`
		Version string `json:"version"`
	}{
		//{"Microsoft Edge", "119.0.2792.52"},
		{"Chromium", fmt.Sprintf("%s.0.6%v.%v", bigVersion, rand1, rand2)},
		{"Not=A?Brand", "24.0.0.0"},
	}
	fp.ClientHint.Mobile = false
	fp.ClientHint.Platform = "Windows"
	fp.ClientHint.PlatformVersion = "10.0.0"
	fp.ClientHint.UaFullVersion = fmt.Sprintf("%s.0.6%v.%v", bigVersion, rand1, rand2)

	// WebGL
	fp.WebGL.Render = generateNvidiaGPUInfo()
	fp.WebGL.Vendor = "Google Inc. (NVIDIA)"
	fp.WebGL.ToDataURL = rand.Intn(200) + 54 // Random value between 100 and 254

	// Navigator
	fp.UserAgent = fmt.Sprintf("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36", bigVersion)
	fp.Platform = "Win32"
	fp.Vendor = "Google Inc."
	//fp.AndroidUid = EncriptMD5(fp.UserAgent)[:16]
	switch browserType {
	case 0:
		attachEdgeFingerPrint(fp, bigVersion, rand1, rand2)

	case 1:
		attach360JsFingerPrint(fp, bigVersion, rand1, rand2)
	case 2:
		attachQQFingerPrint(fp, bigVersion, rand1, rand2)
	case 3:
		attachOperaFingerPrint(fp, bigVersion, rand1, rand2)
	case 4:
		attachEdgeFingerPrint(fp, bigVersion, rand1, rand2)
	case 5:
		attach360FingerPrint(fp, bigVersion, rand1, rand2)
	}
	return fp
}
