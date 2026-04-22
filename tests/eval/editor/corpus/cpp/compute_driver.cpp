// Drives the edited compute() implementation. Expects the signature
//   int compute(int x, bool doubleIt = false);
// where doubleIt=false returns x and doubleIt=true returns x*2.

#include <cassert>
#include <cstdio>

int compute(int x, bool doubleIt);

int main() {
    assert(compute(5, false) == 5);
    assert(compute(10, false) == 10);
    assert(compute(0, false) == 0);
    assert(compute(5, true) == 10);
    assert(compute(10, true) == 20);
    assert(compute(-3, true) == -6);
    std::printf("ok\n");
    return 0;
}
