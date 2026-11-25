# Tâche S2 — câblage multi-fichiers

Le test du module `benchmark_fixture2` échoue : `Square` n'implémente pas
l'interface `Shape` et `NewShape` ignore le type `"square"`.

1. Lis `benchmark_fixture2/shape.go`, `square.go` et `factory.go`.
2. Ajoute la méthode manquante et le cas de fabrique.
3. Vérifie avec `go vet ./benchmark_fixture2/...` puis
   `go test ./benchmark_fixture2/...`.

Contraintes : ne modifie pas `shape_test.go` et ne touche à aucun autre fichier.
