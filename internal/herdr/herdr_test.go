package herdr

import "testing"

func TestCurrentSession(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	cases := []struct{ env, sess, sock, want string }{
		{"", "", "", ""},         // outside herdr
		{"1", "", "", "default"}, // default session leaves HERDR_SESSION empty
		{"1", "", "/cfg/herdr/herdr.sock", "default"},
		{"1", "", "/cfg/herdr/sessions/work/herdr.sock", "work"},
		{"1", "work", "", "work"},
	}
	for _, c := range cases {
		t.Setenv("HERDR_ENV", c.env)
		t.Setenv("HERDR_SESSION", c.sess)
		t.Setenv("HERDR_SOCKET_PATH", c.sock)
		if got := CurrentSession(); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
}
