package chatgpt

import (
	"encoding/binary"
	"reflect"
	"testing"
)

const environmentTestExecutable = "/Applications/ChatGPT.app/Contents/MacOS/ChatGPT"

func processArgsFixture(argv, environment []string) []byte {
	data := make([]byte, 4)
	binary.LittleEndian.PutUint32(data, uint32(len(argv)))
	data = append(data, environmentTestExecutable...)
	data = append(data, 0, 0, 0)
	for _, value := range append(append([]string(nil), argv...), environment...) {
		data = append(data, value...)
		data = append(data, 0)
	}
	return append(data, 0)
}

func TestProcessEnvironmentProjectsOnlyTargetPaths(t *testing.T) {
	data := processArgsFixture([]string{"ChatGPT", "", "CODEX_HOME=/not/environment"}, []string{
		"PRIVATE_TOKEN=DO_NOT_RETAIN", "HOME=/target/home", "CODEX_HOME=/target/custom", "OTHER=value",
	})
	got, err := parseProcessEnvironment(data, environmentTestExecutable)
	want := targetEnvironment{Home: environmentValue{Value: "/target/home", Present: true}, CodexHome: environmentValue{Value: "/target/custom", Present: true}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("projection=%+v err=%v", got, err)
	}
}

func TestProcessEnvironmentPreservesUnsetAndEmpty(t *testing.T) {
	for _, present := range []bool{false, true} {
		env := []string{"HOME=/target/home"}
		if present {
			env = append(env, "CODEX_HOME=")
		}
		got, err := parseProcessEnvironment(processArgsFixture([]string{"ChatGPT"}, env), environmentTestExecutable)
		if err != nil || got.CodexHome.Present != present || got.CodexHome.Value != "" {
			t.Fatalf("present=%t projection=%+v err=%v", present, got, err)
		}
	}
}

func TestProcessEnvironmentRejectsMalformedPayload(t *testing.T) {
	valid := processArgsFixture([]string{"ChatGPT"}, []string{"HOME=/target/home"})
	badArgc := append([]byte(nil), valid...)
	binary.LittleEndian.PutUint32(badArgc, 999999)
	for _, data := range [][]byte{
		nil, valid[:3], valid[:len(valid)-1], badArgc,
		processArgsFixture(nil, []string{"HOME=/target/home"}),
		processArgsFixture([]string{"ChatGPT"}, []string{"HOME=/one", "HOME=/two"}),
		processArgsFixture([]string{"ChatGPT"}, []string{"CODEX_HOME=/one", "CODEX_HOME=/two"}),
		processArgsFixture([]string{"ChatGPT"}, []string{"HOME=/bad\xff"}),
	} {
		if _, err := parseProcessEnvironment(data, environmentTestExecutable); err == nil {
			t.Fatal("accepted malformed process environment")
		}
	}
	if _, err := parseProcessEnvironment(valid, "/another/app"); err == nil {
		t.Fatal("accepted a different executable")
	}
}

func TestConfigHomeUsesTargetEnvironmentOnly(t *testing.T) {
	t.Setenv("HOME", "/host/home")
	t.Setenv("CODEX_HOME", "/host/config")
	for _, test := range []struct {
		name string
		env  targetEnvironment
		want string
	}{
		{"explicit", targetEnvironment{CodexHome: environmentValue{Value: "/target/custom", Present: true}}, "/target/custom"},
		{"unset", targetEnvironment{Home: environmentValue{Value: "/target/home", Present: true}}, "/target/home/.codex"},
		{"empty", targetEnvironment{Home: environmentValue{Value: "/target/home", Present: true}, CodexHome: environmentValue{Present: true}}, ""},
		{"relative", targetEnvironment{CodexHome: environmentValue{Value: "relative", Present: true}}, ""},
		{"no target home", targetEnvironment{}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := configHomeFromEnvironment(test.env)
			if test.want == "" {
				if err == nil || got != "" {
					t.Fatal("accepted unverifiable target path")
				}
			} else if err != nil || got != test.want {
				t.Fatalf("path=%q err=%v, want target path %q", got, err, test.want)
			}
		})
	}
}
