package notify

import "testing"

func TestRenderLocales(t *testing.T) {
	for _, sp := range Catalog {
		for _, l := range []string{"zh-CN", "en"} {
			title, body := Render(sp.Event, l, map[string]string{"name": "A"})
			if title == "" || body == "" || title == sp.Event {
				t.Fatalf("%s/%s: %q %q", sp.Event, l, title, body)
			}
		}
	}
	title, _ := Render(BalanceLow, "en-US", map[string]string{"threshold": usd(5_000_000)})
	if title != "Balance below $5.00" {
		t.Fatalf("en title %q", title)
	}
	title, _ = Render(BalanceLow, "zh-CN", map[string]string{"threshold": usd(5_000_000)})
	if title != "余额低于 $5.00" {
		t.Fatalf("zh title %q", title)
	}
	if NormLocale("") != "zh-CN" || NormLocale("en") != "en" {
		t.Fatal("NormLocale")
	}
}

func TestDefaultsAndVisibility(t *testing.T) {
	want := map[string]bool{AccountAuthError: true, AccountStale: true, SubscriptionPending: true, ReconcileMismatch: true,
		StreamBacklogHigh: true, AccountFull: false, SafeguardEvent: false, BalanceLow: true, PlanExpiring: true,
		AutoRenewFailed: true, KeyExhausted: false}
	if len(Catalog) != len(want) {
		t.Fatalf("catalog %d", len(Catalog))
	}
	for _, sp := range Catalog {
		if sp.DefaultEnabled != want[sp.Event] {
			t.Fatalf("%s default %v", sp.Event, sp.DefaultEnabled)
		}
		if sp.Group == GroupAdmin && Visible(sp, "user") {
			t.Fatalf("%s visible to users", sp.Event)
		}
		if !Visible(sp, "admin") {
			t.Fatalf("%s hidden from admins", sp.Event)
		}
	}
}

func TestDeviceLabel(t *testing.T) {
	cases := map[string]string{
		"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1": "iPhone · Safari",
		"Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Mobile Safari/537.36":                       "Android · Chrome",
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36 Edg/140.0":                   "Windows · Edge",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 15.0; rv:140.0) Gecko/20100101 Firefox/140.0":                                                     "Mac · Firefox",
		"": "Unknown device",
	}
	for ua, want := range cases {
		if got := DeviceLabel(ua); got != want {
			t.Errorf("%q: %q want %q", ua, got, want)
		}
	}
}

func TestValidEndpoint(t *testing.T) {
	s := &Service{}
	for _, ok := range []string{"https://fcm.googleapis.com/fcm/send/abc", "https://web.push.apple.com/QO", "https://updates.push.services.mozilla.com/wpush/v2/x",
		"https://wns2-par02p.notify.windows.com/w/?token=x"} {
		if err := s.ValidEndpoint(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://fcm.googleapis.com/x", "https://evil.example/fcm.googleapis.com", "https://fcm.googleapis.com.evil.example/x", "not a url", "https://127.0.0.1/x"} {
		if err := s.ValidEndpoint(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	s.AllowAnyEndpoint = true
	if err := s.ValidEndpoint("http://127.0.0.1:1234/x"); err != nil {
		t.Fatal(err)
	}
}
