#pragma once

#include <cmath>
#include <stdexcept>

template <typename T>
class RunningStats {
public:
    void add(T value) {
        ++count_;
        sum_ += value;
        sumSq_ += value * value;
    }

    std::size_t size() const { return count_; }

    T mean() const {
        if (count_ == 0) throw std::runtime_error("no samples");
        return sum_ / static_cast<T>(count_);
    }

    T variance() const {
        if (count_ == 0) throw std::runtime_error("no samples");
        T m = mean();
        return (sumSq_ / static_cast<T>(count_)) - (m * m);
    }

private:
    std::size_t count_ = 0;
    T sum_ = T{};
    T sumSq_ = T{};
};
