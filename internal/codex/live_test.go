//go:build live

package codex

import (
	"context"
	"testing"
)

func TestLiveAccount(t *testing.T) {
	c := &Client{Version: "test"}
	defer c.Close()
	a, err := c.Account(context.Background())
	if err != nil || a == nil {
		t.Fatalf("account: %v %v", a != nil, err)
	}
	cr, err := c.Credentials(context.Background(), "")
	t.Logf("plan=%s token=%v account=%v err=%v", a.Plan, cr.Token != "", cr.AccountID != "", err)
	p, s, err := c.Limits(context.Background())
	t.Logf("primary=%+v secondary=%+v err=%v", p, s, err)
}
