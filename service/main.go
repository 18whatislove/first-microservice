package main

import (
	"context"
	"time"
)

const acl = `{
	"logger1":          ["/main.Admin/Logging"],
	"logger2":          ["/main.Admin/Logging"],
	"stat1":            ["/main.Admin/Statistics"],
	"stat2":            ["/main.Admin/Statistics"],
	"biz_user":         ["/main.Biz/Check", "/main.Biz/Add"],
	"biz_admin":        ["/main.Biz/*"],
	"after_disconnect": ["/main.Biz/Add"]
}`

func main() {
	println("usage: make test")
	ctx, _ := context.WithTimeout(context.Background(), 6*time.Second)
	err := StartMyMicroservice(ctx, ":8081", acl)
	if err != nil {
		panic(err)
	}
	time.Sleep(8 * time.Second)
}
