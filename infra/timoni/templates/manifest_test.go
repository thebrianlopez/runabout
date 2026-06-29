package templates

import (
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// renderModule renders the Timoni CUE module to YAML using timoni.
func renderModule(t *testing.T) []map[string]interface{} {
	if _, err := exec.LookPath("timoni"); err != nil {
		t.Skip("timoni not installed")
	}
	// Build command: timoni build [INSTANCE] [MODULE] [FLAGS]
	cmd := exec.Command("timoni", "build", "linkari", "..", "-n", "test", "-o", "yaml")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("timoni build error: %v\noutput: %s", err, string(out))
		t.Fatalf("failed to render manifests with timoni")
	}

	// Parse YAML documents (separated by ---)
	docs := strings.Split(string(out), "---")
	var objects []map[string]interface{}
	for _, doc := range docs {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}
		var obj map[string]interface{}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Logf("failed to parse YAML document: %v", err)
			continue
		}
		objects = append(objects, obj)
	}
	return objects
}

// getObject finds an object by kind and name in the rendered manifests.
func getObject(objs []map[string]interface{}, kind, name string) map[string]interface{} {
	for _, obj := range objs {
		if objKind, ok := obj["kind"].(string); ok && objKind == kind {
			if metadata, ok := obj["metadata"].(map[string]interface{}); ok {
				if objName, ok := metadata["name"].(string); ok && objName == name {
					return obj
				}
			}
		}
	}
	return nil
}

// CT-1: Backup PVC renders with correct spec
func TestManifest_BackupPVCRenders(t *testing.T) {
	objs := renderModule(t)
	backupPvc := getObject(objs, "PersistentVolumeClaim", "linkari-backup")
	if backupPvc == nil {
		t.Fatalf("linkari-backup PVC not found in rendered manifests")
	}

	spec := backupPvc["spec"].(map[string]interface{})

	// Check storage class
	if sc, ok := spec["storageClassName"].(string); !ok || sc != "local-path" {
		t.Errorf("storageClassName: got %v, want local-path", spec["storageClassName"])
	}

	// Check storage size
	resources := spec["resources"].(map[string]interface{})
	requests := resources["requests"].(map[string]interface{})
	if storage, ok := requests["storage"].(string); !ok || storage != "10Gi" {
		t.Errorf("storage: got %v, want 10Gi", requests["storage"])
	}
}

// CT-2: PodDisruptionBudget renders
func TestManifest_PDBRenders(t *testing.T) {
	objs := renderModule(t)
	pdb := getObject(objs, "PodDisruptionBudget", "linkari-pdb")
	if pdb == nil {
		t.Fatalf("PodDisruptionBudget not found in rendered manifests")
	}

	spec := pdb["spec"].(map[string]interface{})

	// Check maxUnavailable
	if maxUnavail, ok := spec["maxUnavailable"].(int); !ok || maxUnavail != 0 {
		t.Errorf("maxUnavailable: got %v, want 0", spec["maxUnavailable"])
	}

	// Check selector
	selector := spec["selector"].(map[string]interface{})
	matchLabels := selector["matchLabels"].(map[string]interface{})
	if _, ok := matchLabels["app.kubernetes.io/name"]; !ok {
		t.Errorf("selector missing app.kubernetes.io/name label")
	}
}

// CT-3: Init container wires restore logic and mounts
func TestManifest_InitContainerWired(t *testing.T) {
	objs := renderModule(t)
	deploy := getObject(objs, "Deployment", "linkari")
	if deploy == nil {
		t.Fatalf("Deployment not found in rendered manifests")
	}

	spec := deploy["spec"].(map[string]interface{})
	template := spec["template"].(map[string]interface{})
	podSpec := template["spec"].(map[string]interface{})

	initContainers, ok := podSpec["initContainers"].([]interface{})
	if !ok || len(initContainers) == 0 {
		t.Fatalf("initContainers not found in Deployment")
	}

	ic := initContainers[0].(map[string]interface{})
	if name, ok := ic["name"].(string); !ok || name != "linkari-db-restore" {
		t.Fatalf("init container name: got %v, want linkari-db-restore", ic["name"])
	}

	mounts := ic["volumeMounts"].([]interface{})
	wantMounts := map[string]string{"linkari-data": "/var/lib/linkari", "linkari-backup": "/var/lib/linkari-backup"}
	for wantName, wantPath := range wantMounts {
		found := false
		for _, mount := range mounts {
			m := mount.(map[string]interface{})
			if name, ok := m["name"].(string); ok && name == wantName {
				if path, ok := m["mountPath"].(string); !ok || path != wantPath {
					t.Errorf("%s mountPath: got %v, want %s", wantName, path, wantPath)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s mount not found in init container", wantName)
		}
	}

	command := ic["command"].([]interface{})
	if len(command) < 3 {
		t.Fatalf("init container command too short: %v", command)
	}
	if flag, ok := command[1].(string); !ok || flag != "-ceu" {
		t.Errorf("shell flags: got %v, want -ceu", command[1])
	}
	script, ok := command[2].(string)
	if !ok {
		t.Fatalf("script arg missing: %v", command[2])
	}
	for _, want := range []string{"skipping restore", "IR-002 k8s_backup_mount_missing", "IR-003 restore_seed_absent", "IR-001 seed complete"} {
		if !strings.Contains(script, want) {
			t.Errorf("init script missing %q", want)
		}
	}
}

// CT-4: Backup volume wired into Deployment
func TestManifest_BackupVolumeWired(t *testing.T) {
	objs := renderModule(t)
	deploy := getObject(objs, "Deployment", "linkari")
	if deploy == nil {
		t.Fatalf("Deployment not found in rendered manifests")
	}

	spec := deploy["spec"].(map[string]interface{})
	template := spec["template"].(map[string]interface{})
	podSpec := template["spec"].(map[string]interface{})
	volumes := podSpec["volumes"].([]interface{})

	// Check for linkari-backup volume
	found := false
	for _, vol := range volumes {
		v := vol.(map[string]interface{})
		if name, ok := v["name"].(string); ok && name == "linkari-backup" {
			found = true
			pvc := v["persistentVolumeClaim"].(map[string]interface{})
			if claimName, ok := pvc["claimName"].(string); !ok || claimName != "linkari-backup" {
				t.Errorf("backup PVC claim name: got %v, want linkari-backup", claimName)
			}
			break
		}
	}
	if !found {
		t.Errorf("linkari-backup volume not wired in Deployment")
	}
}

// CT-5: Live deployment unchanged (regression guard RG-1)
func TestManifest_LiveDeploymentUnchanged(t *testing.T) {
	objs := renderModule(t)
	deploy := getObject(objs, "Deployment", "linkari")
	if deploy == nil {
		t.Fatalf("Deployment not found in rendered manifests")
	}

	spec := deploy["spec"].(map[string]interface{})

	// Check replicas
	if replicas, ok := spec["replicas"].(int); !ok || replicas != 1 {
		t.Errorf("replicas: got %v, want 1", spec["replicas"])
	}

	// Check strategy is Recreate
	strategy := spec["strategy"].(map[string]interface{})
	if stratType, ok := strategy["type"].(string); !ok || stratType != "Recreate" {
		t.Errorf("strategy.type: got %v, want Recreate", strategy["type"])
	}

	// Check linkari-data volume and mount still exist
	template := spec["template"].(map[string]interface{})
	podSpec := template["spec"].(map[string]interface{})
	volumes := podSpec["volumes"].([]interface{})

	dataVolFound := false
	for _, vol := range volumes {
		v := vol.(map[string]interface{})
		if name, ok := v["name"].(string); ok && name == "linkari-data" {
			dataVolFound = true
			break
		}
	}
	if !dataVolFound {
		t.Errorf("linkari-data volume not found (should be unchanged)")
	}

	// Check linkari-data mount at /var/lib/linkari
	containers := podSpec["containers"].([]interface{})
	container := containers[0].(map[string]interface{})
	mounts := container["volumeMounts"].([]interface{})
	dataMountFound := false
	for _, mount := range mounts {
		m := mount.(map[string]interface{})
		if name, ok := m["name"].(string); ok && name == "linkari-data" {
			if path, ok := m["mountPath"].(string); ok && path == "/var/lib/linkari" {
				dataMountFound = true
			}
			break
		}
	}
	if !dataMountFound {
		t.Errorf("linkari-data mount at /var/lib/linkari not found")
	}
}

// CT-6: Schema validates (basic structural check - full kubeconform validation in CI)
func TestManifest_SchemaValidation(t *testing.T) {
	objs := renderModule(t)

	// Check that required objects exist and have expected structure
	requiredKinds := []string{"PersistentVolumeClaim", "PodDisruptionBudget", "Deployment", "ConfigMap", "Service"}
	for _, kind := range requiredKinds {
		found := false
		for _, obj := range objs {
			if objKind, ok := obj["kind"].(string); ok && objKind == kind {
				found = true
				// Check that metadata exists
				if _, ok := obj["metadata"].(map[string]interface{}); !ok {
					t.Errorf("object %s missing metadata", kind)
				}
				break
			}
		}
		if !found {
			t.Errorf("required object kind %s not found", kind)
		}
	}
}
