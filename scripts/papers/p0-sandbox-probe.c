/* Trusted, bounded P0 resource probe. Never expose this tool to user LaTeX. */
#define _POSIX_C_SOURCE 200809L
#include <errno.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

static double seconds(clockid_t id) {
    struct timespec t;
    if (clock_gettime(id, &t) != 0) { perror("clock_gettime"); exit(3); }
    return (double)t.tv_sec + (double)t.tv_nsec / 1e9;
}
static int cpu_test(void) {
    const double start_wall = seconds(CLOCK_MONOTONIC);
    const double start_cpu = seconds(CLOCK_PROCESS_CPUTIME_ID);
    volatile uint64_t counter = 0;
    while (seconds(CLOCK_MONOTONIC) - start_wall < 3.0) { counter++; }
    const double wall = seconds(CLOCK_MONOTONIC) - start_wall;
    const double cpu = seconds(CLOCK_PROCESS_CPUTIME_ID) - start_cpu;
    const double ratio = cpu / wall;
    printf("P0_CPU_WALL=%.3f P0_CPU_USED=%.3f P0_CPU_RATIO=%.3f count=%llu\n",
           wall, cpu, ratio, (unsigned long long)counter);
    fflush(stdout);
    return (ratio >= 0.10 && ratio <= 0.80) ? 0 : 4;
}
static int memory_test(void) {
    const size_t bytes = 320UL * 1024 * 1024;
    volatile unsigned char *mem = malloc(bytes);
    if (!mem) { puts("P0_MEMORY_ALLOC_FAILED"); return 5; }
    volatile unsigned long checksum = 0;
    for (size_t i = 0; i < bytes; i += 4096) {
        mem[i] = (unsigned char)((i / 4096) % 251);
        checksum += mem[i];
    }
    printf("P0_MEMORY_LIMIT_FAILED: wrote %zu bytes, checksum=%lu\n", bytes, checksum);
    fflush(stdout);
    free((void *)mem);
    return 6;
}
static int pids_test(void) {
    pid_t children[160];
    int spawned = 0, saved_errno = 0;
    for (int i = 0; i < 160; ++i) {
        pid_t pid = fork();
        if (pid < 0) { saved_errno = errno; break; }
        if (pid == 0) { sleep(8); _exit(0); }
        children[spawned++] = pid;
        if (spawned % 16 == 0) {
            printf("P0_PIDS_PROGRESS=%d\n", spawned);
            fflush(stdout);
        }
    }
    for (int i = 0; i < spawned; ++i) kill(children[i], SIGKILL);
    for (int i = 0; i < spawned; ++i) waitpid(children[i], NULL, 0);
    printf("P0_PIDS_SPAWNED=%d P0_PIDS_FAILURE_ERRNO=%d\n", spawned, saved_errno);
    fflush(stdout);
    return (spawned > 0 && spawned < 160 && saved_errno == EAGAIN) ? 0 : 7;
}
int main(int argc, char **argv) {
    fprintf(stderr, "P0_PROBE_STARTED argc=%d argv0=%s argv1=%s\n",
            argc, argc > 0 ? argv[0] : "(none)", argc > 1 ? argv[1] : "(none)");
    fflush(stderr);
    if (argc != 2) return 2;
    if (strcmp(argv[1], "cpu") == 0) return cpu_test();
    if (strcmp(argv[1], "memory") == 0) return memory_test();
    if (strcmp(argv[1], "pids") == 0) return pids_test();
    if (strcmp(argv[1], "timeout") == 0) { sleep(15); puts("P0_TIMEOUT_FAILED"); return 8; }
    return 2;
}
