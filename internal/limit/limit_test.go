package limit

import "testing"

func TestPerOwner(t *testing.T) {
	p := &PerOwner{Max: 2}
	r1, ok1 := p.Take("a")
	_, ok2 := p.Take("a")
	_, ok3 := p.Take("a")
	_, okB := p.Take("b")
	if !ok1 || !ok2 || ok3 || !okB {
		t.Fatalf("take = %v %v %v %v; want the third of a's refused and b's own", ok1, ok2, ok3, okB)
	}
	r1()
	if _, ok := p.Take("a"); !ok {
		t.Error("a released one but can't take it again")
	}
}
