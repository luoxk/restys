package main

import (
	"fmt"
	"log"
	"restys"
	"time"
)

func tc() *restys.Client {
	return restys.C().
		EnableInsecureSkipVerify()
}

func main() {
	client := tc().
		SetCommonRetryCount(2).
		SetCommonRetryBackoffInterval(1*time.Second, 5*time.Second).
		ImpersonateChrome().
		SetJa3WithStr("771,4865-4866-4867-49195-49199-49196-49200-52393-52392-49171-49172-156-157-47-53,51-16-11-10-18-45-35-17513-27-23-0-43-65037-65281-13-5,4588-29-23-24,0").
		SetAkamaiWithStr("1:65536,2:0,4:6291456,6:262144|15663106|0|m,a,s,p")

	res, err := client.R().Get("https://tls.browserleaks.com/json")
	if err != nil {
		log.Println(err)
	}
	fmt.Println(res.String())
}
