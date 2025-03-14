package compose

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"golang.org/x/sync/errgroup"
	"gopkg.in/yaml.v3"
)

func TestAddToEmpty(t *testing.T) {
	// Arrange
	dest := "test_addtoempty.yaml"

	file, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())

	container := Container{
		Name: "test",
	}

	// Act
	err = Add(dest, "test", &container)
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	verify, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}

	var comp DockerCompose
	err = yaml.Unmarshal(verify, &comp)
	if err != nil {
		t.Fatal(err)
	}

	if len(comp.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(comp.Services))
	}

	if comp.Services["test"].Name != "test" {
		t.Fatalf("expected service name to be test, got %s", comp.Services["test"].Name)
	}
}

func TestAddToExisting(t *testing.T) {
	// Arrange
	dest := "test_addtoexisting.yaml"

	file, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())

	currentContainer := Container{
		Name: "current",
	}

	err = Add(dest, "current", &currentContainer)
	if err != nil {
		t.Fatal(err)
	}

	newContainer := Container{
		Name: "new",
	}

	// Act
	err = Add(dest, "new", &newContainer)
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	verify, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}

	var comp DockerCompose
	err = yaml.Unmarshal(verify, &comp)
	if err != nil {
		t.Fatal(err)
	}

	if len(comp.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(comp.Services))
	}

	if s, ok := comp.Services["current"]; !ok || s.Name != "current" {
		t.Fatalf("expected service 'current' to exist, got %+v/n", comp.Services)
	}

	if s, ok := comp.Services["new"]; !ok || s.Name != "new" {
		t.Fatalf("expected service 'new' to exist, got %+v/n", comp.Services)
	}
}

func TestRemoveLast(t *testing.T) {
	// Arrange
	dest := "test_removelast.yaml"

	file, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())

	container := Container{
		Name: "test",
	}

	err = Add(dest, "test", &container)
	if err != nil {
		t.Fatal(err)
	}

	verify, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}

	var comp DockerCompose
	err = yaml.Unmarshal(verify, &comp)
	if err != nil {
		t.Fatal(err)
	}

	if len(comp.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(comp.Services))
	}

	if comp.Services["test"].Name != "test" {
		t.Fatalf("expected service name to be test, got %s", comp.Services["test"].Name)
	}

	// Act
	err = RemoveClone(dest, []string{"test"})
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	verify, err = os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}

	err = yaml.Unmarshal(verify, &comp)
	if err != nil {
		t.Fatal(err)
	}

	if len(comp.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(comp.Services))
	}

	if comp.Services["test"].Name != "test" {
		t.Fatalf("expected service name to be test, got %s", comp.Services["test"].Name)
	}
}

func TestRemoveOneFromList(t *testing.T) {
	// Arrange
	dest := "test_removeonefromlist.yaml"

	file, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())

	c1 := Container{
		Name: "c1",
	}

	err = Add(dest, "c1", &c1)
	if err != nil {
		t.Fatal(err)
	}

	c2 := Container{
		Name: "c2",
	}

	err = Add(dest, "c2", &c2)
	if err != nil {
		t.Fatal(err)
	}

	// Act
	err = RemoveClone(dest, []string{"c1"})
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	verify, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}

	var comp DockerCompose
	err = yaml.Unmarshal(verify, &comp)
	if err != nil {
		t.Fatal(err)
	}

	if len(comp.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(comp.Services))
	}

	if s, ok := comp.Services["c2"]; !ok || s.Name != "c2" {
		t.Fatalf("expected service 'c2' to exist, got %+v/n", comp.Services)
	}
}

func TestRemoveMultiple(t *testing.T) {
	// Arrange
	dest := "test_removemultiple.yaml"

	file, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())

	c1 := Container{
		Name: "c1",
	}

	err = Add(dest, "c1", &c1)
	if err != nil {
		t.Fatal(err)
	}

	c2 := Container{
		Name: "c2",
	}

	err = Add(dest, "c2", &c2)
	if err != nil {
		t.Fatal(err)
	}

	verify, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}

	var actComp DockerCompose
	err = yaml.Unmarshal(verify, &actComp)
	if err != nil {
		t.Fatal(err)
	}

	if len(actComp.Services) != 2 {
		t.Fatalf("expected 2 services, got %d", len(actComp.Services))
	}

	if s, ok := actComp.Services["c1"]; !ok || s.Name != "c1" {
		t.Fatalf("expected service name to be c1, got %s", actComp.Services["c1"].Name)
	}

	if s, ok := actComp.Services["c2"]; !ok || s.Name != "c2" {
		t.Fatalf("expected service name to be c2, got %s", actComp.Services["c2"].Name)
	}

	// Act
	err = RemoveClone(dest, []string{"c1", "c2"})
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	verify, err = os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}

	var comp DockerCompose
	err = yaml.Unmarshal(verify, &comp)
	if err != nil {
		t.Fatal(err)
	}

	if len(comp.Services) != 0 {
		t.Fatalf("expected 0 services, got %d", len(comp.Services))
	}
}

func TestAddConcurrent(t *testing.T) {
	// Arrange
	dest := "test_addconcurrent.yaml"

	file, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())

	// Act
	errs := errgroup.Group{}
	for i := 0; i < 100; i++ {
		errs.Go(func() error {
			container := Container{
				Name: fmt.Sprintf("test-%d", i),
			}
			return Add(dest, fmt.Sprintf("test-%d", i), &container)
		})
	}

	err = errs.Wait()
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	verify, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}

	var comp DockerCompose
	err = yaml.Unmarshal(verify, &comp)
	if err != nil {
		t.Fatal(err)
	}

	if len(comp.Services) != 100 {
		t.Fatalf("expected 100 services, got %d", len(comp.Services))
	}

	for i := 0; i < 100; i++ {
		if s, ok := comp.Services[fmt.Sprintf("test-%d", i)]; !ok || s.Name != fmt.Sprintf("test-%d", i) {
			t.Fatalf("expected service name to be test-%d, got %s", i, s.Name)
		}
	}
}

func TestRemoveConcurrent(t *testing.T) {
	// Arrange
	dest := "test_removeconcurrent.yaml"

	file, err := os.Create(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())

	wgAdd := sync.WaitGroup{}
	for i := 0; i < 100; i++ {
		wgAdd.Add(1)
		go func(i int) error {
			defer wgAdd.Done()
			container := Container{
				Name: fmt.Sprintf("test-%d", i),
			}
			return Add(dest, fmt.Sprintf("test-%d", i), &container)
		}(i)
	}

	wgAdd.Wait()

	verify, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}

	var actComp DockerCompose
	err = yaml.Unmarshal(verify, &actComp)
	if err != nil {
		t.Fatal(err)
	}

	if len(actComp.Services) != 100 {
		t.Fatalf("expected 100 services, got %d", len(actComp.Services))
	}

	// Act
	wgRemove := sync.WaitGroup{}
	for i := 0; i < 100; i++ {
		wgRemove.Add(1)
		go func(i int) error {
			defer wgRemove.Done()
			if i == 37 {
				// Spot check
				return nil
			}
			return RemoveClone(dest, []string{fmt.Sprintf("test-%d", i)})
		}(i)
		if err != nil {
			t.Fatal(err)
		}
	}

	wgRemove.Wait()

	// Assert
	verify, err = os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}

	var comp DockerCompose
	err = yaml.Unmarshal(verify, &comp)
	if err != nil {
		t.Fatal(err)
	}

	if len(comp.Services) != 1 {
		t.Fatalf("expected 1 services, got %d", len(comp.Services))
	}

	if s, ok := comp.Services["test-37"]; !ok || s.Name != "test-37" {
		t.Fatalf("expected service name to be test-37, got %s", s.Name)
	}
}
