package profiles

import "testing"

func TestReleasedProfilesUseExactOfficialModules(t *testing.T) {
	for _, name := range []string{"default", "minimal"} {
		profile, err := Lookup(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(profile.Plugins) == 0 {
			t.Fatalf("profile %s is empty", name)
		}
		seenModules := map[string]bool{}
		seenNames := map[string]bool{}
		for _, plugin := range profile.Plugins {
			if plugin.Version != "v0.1.0" {
				t.Fatalf("profile %s plugin %s version = %s", name, plugin.Module, plugin.Version)
			}
			if seenModules[plugin.Module] {
				t.Fatalf("profile %s repeats module %s", name, plugin.Module)
			}
			if seenNames[plugin.Name] {
				t.Fatalf("profile %s repeats name %s", name, plugin.Name)
			}
			seenModules[plugin.Module] = true
			seenNames[plugin.Name] = true
		}
	}
}

func TestLookupRejectsUnknownProfile(t *testing.T) {
	if _, err := Lookup("does-not-exist"); err == nil {
		t.Fatal("unknown profile must fail")
	}
}
