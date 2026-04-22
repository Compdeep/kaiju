cmake_minimum_required(VERSION 3.20)
project(sampleapp CXX)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)

add_executable(sampleapp
    src/main.cpp
    src/core.cpp
)

target_include_directories(sampleapp PRIVATE include)
