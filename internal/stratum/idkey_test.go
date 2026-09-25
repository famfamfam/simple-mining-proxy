package stratum

import "testing"

func TestIDKeyMatchesNumberEchoedAsString(t *testing.T) {
	cases := map[string]string{
		`{"id":7,"result":true}`:      "7",
		`{"id":"7","result":true}`:    "7",
		`{"id":"abc","result":true}`:  `"abc"`,
		`{"id":"","result":true}`:     `""`,
		`{"id":"-7","result":true}`:   `"-7"`,
		`{"id":null,"method":"x"}`:    "",
		`{"method":"mining.notify"}`:  "",
		`{"id":"0012","result":true}`: "0012",
	}
	for line, want := range cases {
		m, err := Parse([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		if got := m.IDKey(); got != want {
			t.Errorf("%s: IDKey = %q, want %q", line, got, want)
		}
	}
}
