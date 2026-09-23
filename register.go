package lark

import "go.gh.ink/notifyutils/driver"

func init() {
	driver.Register(Name, Driver{})
}
