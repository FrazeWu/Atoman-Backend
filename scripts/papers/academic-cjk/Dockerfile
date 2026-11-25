FROM atoman-papers-texlive:academic-verified-20261124
USER root
RUN set -eu; \
    tlmgr --verify-repo=all --repository https://mirror.math.princeton.edu/pub/CTAN/systems/texlive/tlnet install xetex ctex xecjk; \
    test -x /usr/local/texlive/2026/bin/x86_64-linux/xetex; \
    test -x /usr/local/texlive/2026/bin/x86_64-linux/xelatex; \
    ln -s /usr/local/texlive/2026/bin/x86_64-linux/xetex /usr/bin/xetex; \
    ln -s /usr/local/texlive/2026/bin/x86_64-linux/xelatex /usr/bin/xelatex; \
    fmtutil-sys --byfmt xelatex; \
    test -n "$(kpsewhich ctexart.cls)"; \
    xelatex --version
RUN set -eu; \
    apt-get -o Acquire::Retries=2 update; \
    apt-get install -y --no-install-recommends fonts-noto-cjk; \
    fc-cache -f; \
    fc-match 'Noto Serif CJK SC' | grep -q 'Noto Serif CJK SC'; \
    fc-match 'Noto Sans CJK SC' | grep -q 'Noto Sans CJK SC'; \
    rm -rf /var/lib/apt/lists/*
