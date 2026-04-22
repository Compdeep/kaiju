// Driver for the edited algorithm.cpp. Expects:
//   std::vector<int> top_k(const std::vector<int>&, int);
// After the edit, top_k should throw std::invalid_argument when k is negative.

#include <cstdio>
#include <stdexcept>
#include <vector>

std::vector<int> top_k(const std::vector<int>&, int);

int main() {
    auto r = top_k({5, 2, 9, 1}, 2);
    if (r.size() != 2 || r[0] != 9 || r[1] != 5) {
        std::fprintf(stderr, "happy-path wrong: size=%zu\n", r.size());
        return 1;
    }
    bool threw = false;
    try {
        top_k({1, 2, 3}, -1);
    } catch (const std::invalid_argument&) {
        threw = true;
    }
    if (!threw) {
        std::fprintf(stderr, "negative k did not throw std::invalid_argument\n");
        return 2;
    }
    std::puts("ok");
    return 0;
}
