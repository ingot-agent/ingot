package buildinfo

import "testing"

func TestCurrentUsesInjectedMetadata(t *testing.T) {
	oldVersion, oldOfficial := CoreVersion, Official
	oldRevision, oldModified := Revision, Modified
	t.Cleanup(func() {
		CoreVersion, Official = oldVersion, oldOfficial
		Revision, Modified = oldRevision, oldModified
	})
	CoreVersion = "1.2.3"
	Official = "true"
	Revision = "0123456789abcdef0123456789abcdef01234567"
	Modified = "false"

	info := Current()
	if info.CoreVersion != CoreVersion || !info.Official || info.Revision != Revision || info.Modified {
		t.Fatalf("Current() = %#v", info)
	}
	if info.GoVersion == "" || info.Target == "" {
		t.Fatalf("Current() missing runtime facts: %#v", info)
	}
}
