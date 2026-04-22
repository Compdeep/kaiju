#include <vector>
#include <algorithm>
#include <stdexcept>

std::vector<int> top_k(const std::vector<int>& input, int k) {
    std::vector<int> result(input);
    std::sort(result.begin(), result.end(), std::greater<int>());
    if (static_cast<int>(result.size()) > k) {
        result.resize(k);
    }
    return result;
}
