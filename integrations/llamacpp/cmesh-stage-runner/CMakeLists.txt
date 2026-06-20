set(TARGET cmesh-stage-runner)

add_executable(${TARGET} cmesh-stage-runner.cpp)
target_link_libraries(${TARGET} PRIVATE llama)
target_compile_features(${TARGET} PRIVATE cxx_std_17)

if(LLAMA_TOOLS_INSTALL)
    install(TARGETS ${TARGET} RUNTIME)
endif()
