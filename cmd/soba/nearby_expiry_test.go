package main

import (
	"strings"
	"testing"
)

func TestNearbyExpiryGuidancePreservesOriginalSession(t *testing.T) {
	for _, tc := range []struct {
		code string
		en   []string
		ja   []string
	}{
		{"setup_candidate_expired", []string{"original nearby status", "cancel its temporary setup", "both devices"}, []string{"元の nearby status", "一時的なセットアップを取り消し", "両端末"}},
		{"setup_session_expired", []string{"original status and cleanup", "new consent", "Do not repeat the expired approval"}, []string{"元の状態と終了処理", "改めて許可", "期限切れの承認を繰り返さない"}},
	} {
		for _, ja := range []bool{false, true} {
			err := nearbyError(ja, tc.code)
			if nearbyErrorCode(t, err) != tc.code || strings.Contains(err.Error(), "TTL") {
				t.Fatal("expiry guidance changed its code or exposed transport detail")
			}
			want := tc.en
			if ja {
				want = tc.ja
			}
			for _, phrase := range want {
				if !strings.Contains(err.Error(), phrase) {
					t.Fatal("expiry guidance omitted its next action")
				}
			}
		}
	}
}
