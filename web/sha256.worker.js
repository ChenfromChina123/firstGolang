/**
 * SHA256 Web Worker - 在后台线程计算文件完整 SHA256 哈希
 *
 * 用途：秒传功能需要完整文件 SHA256（64 hex 字符），主线程计算大文件会阻塞 UI。
 * Worker 分块读取文件并增量计算，通过 postMessage 返回结果。
 *
 * 消息协议：
 *   主线程 → Worker: { type: "hash", file: File, chunkSize: number }
 *   Worker → 主线程: { type: "progress", percent: number }  （进度通知，可选）
 *   Worker → 主线程: { type: "done", hash: string }          （完成，64 hex 字符）
 *   Worker → 主线程: { type: "error", error: string }        （失败）
 */

/**
 * SHA256 增量计算器（纯 JS 实现，无外部依赖）
 * 算法基于 FIPS 180-4，支持分块 update，最后 hex 输出。
 */
function SHA256() {
    // 初始哈希值（FIPS 180-4 5.3.1）
    this.h = new Uint32Array([
        0x6a09e667, 0xbb67ae85, 0x3c6ef372, 0xa54ff53a,
        0x510e527f, 0x9b05688c, 0x1f83d9ab, 0x5be0cd19
    ]);
    // 缓冲区（累积不足 64 字节的部分）
    this.buffer = new Uint8Array(64);
    this.bufferLen = 0;
    // 总处理字节数（用于最终 padding）
    this.totalLen = 0;
}

// SHA256 常量表 K（FIPS 180-4 4.2.2）
SHA256.prototype.K = new Uint32Array([
    0x428a2f98, 0x71374491, 0xb5c0fbcf, 0xe9b5dba5, 0x3956c25b, 0x59f111f1, 0x923f82a4, 0xab1c5ed5,
    0xd807aa98, 0x12835b01, 0x243185be, 0x550c7dc3, 0x72be5d74, 0x80deb1fe, 0x9bdc06a7, 0xc19bf174,
    0xe49b69c1, 0xefbe4786, 0x0fc19dc6, 0x240ca1cc, 0x2de92c6f, 0x4a7484aa, 0x5cb0a9dc, 0x76f988da,
    0x983e5152, 0xa831c66d, 0xb00327c8, 0xbf597fc7, 0xc6e00bf3, 0xd5a79147, 0x06ca6351, 0x14292967,
    0x27b70a85, 0x2e1b2138, 0x4d2c6dfc, 0x53380d13, 0x650a7354, 0x766a0abb, 0x81c2c92e, 0x92722c85,
    0xa2bfe8a1, 0xa81a664b, 0xc24b8b70, 0xc76c51a3, 0xd192e819, 0xd6990624, 0xf40e3585, 0x106aa070,
    0x19a4c116, 0x1e376c08, 0x2748774c, 0x34b0bcb5, 0x391c0cb3, 0x4ed8aa4a, 0x5b9cca4f, 0x682e6ff3,
    0x748f82ee, 0x78a5636f, 0x84c87814, 0x8cc70208, 0x90befffa, 0xa4506ceb, 0xbef9a3f7, 0xc67178f2
]);

// 循环右移
SHA256.prototype.rotr = function (n, x) {
    return (x >>> n) | (x << (32 - n));
};

// 处理一个 64 字节块（FIPS 180-4 6.2.2）
SHA256.prototype.processBlock = function (block) {
    var w = new Uint32Array(64);
    var i;
    // 前 16 个 word 直接从块中读取（大端序）
    for (i = 0; i < 16; i++) {
        w[i] = (block[i * 4] << 24) | (block[i * 4 + 1] << 16) |
               (block[i * 4 + 2] << 8) | block[i * 4 + 3];
    }
    // 扩展后 48 个 word
    for (i = 16; i < 64; i++) {
        var s0 = this.rotr(7, w[i - 15]) ^ this.rotr(18, w[i - 15]) ^ (w[i - 15] >>> 3);
        var s1 = this.rotr(17, w[i - 2]) ^ this.rotr(19, w[i - 2]) ^ (w[i - 2] >>> 10);
        w[i] = (w[i - 16] + s0 + w[i - 7] + s1) >>> 0;
    }

    // 初始化工作变量
    var a = this.h[0], b = this.h[1], c = this.h[2], d = this.h[3];
    var e = this.h[4], f = this.h[5], g = this.h[6], hh = this.h[7];

    // 64 轮压缩
    for (i = 0; i < 64; i++) {
        var S1 = this.rotr(6, e) ^ this.rotr(11, e) ^ this.rotr(25, e);
        var ch = (e & f) ^ (~e & g);
        var temp1 = (hh + S1 + ch + this.K[i] + w[i]) >>> 0;
        var S0 = this.rotr(2, a) ^ this.rotr(13, a) ^ this.rotr(22, a);
        var maj = (a & b) ^ (a & c) ^ (b & c);
        var temp2 = (S0 + maj) >>> 0;

        hh = g; g = f; f = e;
        e = (d + temp1) >>> 0;
        d = c; c = b; b = a;
        a = (temp1 + temp2) >>> 0;
    }

    // 更新哈希值
    this.h[0] = (this.h[0] + a) >>> 0;
    this.h[1] = (this.h[1] + b) >>> 0;
    this.h[2] = (this.h[2] + c) >>> 0;
    this.h[3] = (this.h[3] + d) >>> 0;
    this.h[4] = (this.h[4] + e) >>> 0;
    this.h[5] = (this.h[5] + f) >>> 0;
    this.h[6] = (this.h[6] + g) >>> 0;
    this.h[7] = (this.h[7] + hh) >>> 0;
};

// 增量更新：追加数据到哈希计算
SHA256.prototype.update = function (data) {
    var offset = 0;
    // 先填充缓冲区剩余空间
    if (this.bufferLen > 0) {
        var need = 64 - this.bufferLen;
        var copy = Math.min(need, data.length);
        this.buffer.set(data.subarray(0, copy), this.bufferLen);
        this.bufferLen += copy;
        offset = copy;
        if (this.bufferLen === 64) {
            this.processBlock(this.buffer);
            this.bufferLen = 0;
        }
    }
    // 处理完整的 64 字节块
    while (offset + 64 <= data.length) {
        this.processBlock(data.subarray(offset, offset + 64));
        offset += 64;
    }
    // 剩余不足 64 字节存入缓冲区
    if (offset < data.length) {
        var remaining = data.length - offset;
        this.buffer.set(data.subarray(offset), 0);
        this.bufferLen = remaining;
    }
    this.totalLen += data.length;
};

// 完成计算：添加 padding 并输出 hex 字符串
SHA256.prototype.hex = function () {
    // 保存 totalLen（bit），注意 JS Number 精度：2^53 以内安全，即文件 < 1 PB 没问题
    var bitLen = this.totalLen * 8;
    // padding: 0x80 + 0x00...0x00 + 8 字节长度（大端序）
    var padLen = (this.bufferLen < 56) ? (56 - this.bufferLen) : (120 - this.bufferLen);
    var padding = new Uint8Array(padLen + 8);
    padding[0] = 0x80;
    // 末尾 8 字节为 bitLen 的大端表示（仅低 32 位有效，文件 < 512MB 时高 32 位为 0）
    // 为支持大文件，用两个 32 位分别处理
    var highBits = Math.floor(bitLen / 0x100000000);
    var lowBits = bitLen >>> 0;
    padding[padLen] = (highBits >>> 24) & 0xff;
    padding[padLen + 1] = (highBits >>> 16) & 0xff;
    padding[padLen + 2] = (highBits >>> 8) & 0xff;
    padding[padLen + 3] = highBits & 0xff;
    padding[padLen + 4] = (lowBits >>> 24) & 0xff;
    padding[padLen + 5] = (lowBits >>> 16) & 0xff;
    padding[padLen + 6] = (lowBits >>> 8) & 0xff;
    padding[padLen + 7] = lowBits & 0xff;
    this.update(padding);

    // 输出 8 个哈希值为 64 hex 字符
    var hex = '';
    for (var i = 0; i < 8; i++) {
        var h = this.h[i];
        hex += ((h >>> 24) & 0xff).toString(16).padStart(2, '0');
        hex += ((h >>> 16) & 0xff).toString(16).padStart(2, '0');
        hex += ((h >>> 8) & 0xff).toString(16).padStart(2, '0');
        hex += (h & 0xff).toString(16).padStart(2, '0');
    }
    return hex;
};

/**
 * 计算文件完整 SHA256（分块读取，增量计算）
 * @param {File} file - 待计算文件
 * @param {number} chunkSize - 分块大小（字节），默认 4MB
 * @param {function} onProgress - 进度回调，参数为 0-100
 * @returns {Promise<string>} 64 字符 hex SHA256
 */
async function computeFileSHA256(file, chunkSize, onProgress) {
    var hasher = new SHA256();
    var offset = 0;
    var total = file.size;

    while (offset < total) {
        var end = Math.min(offset + chunkSize, total);
        var chunk = file.slice(offset, end);
        var buf = await chunk.arrayBuffer();
        hasher.update(new Uint8Array(buf));
        offset = end;
        if (onProgress) {
            onProgress(Math.round((offset / total) * 100));
        }
        // 让出事件循环，避免 Worker 长时间占用
        await new Promise(function (resolve) { setTimeout(resolve, 0); });
    }

    return hasher.hex();
}

// Worker 消息处理
self.onmessage = async function (e) {
    var msg = e.data;
    if (msg.type !== 'hash') return;

    var file = msg.file;
    var chunkSize = msg.chunkSize || (4 * 1024 * 1024); // 默认 4MB

    if (!file || !file.size) {
        self.postMessage({ type: 'error', error: 'invalid file' });
        return;
    }

    try {
        var lastReport = -1;
        var hash = await computeFileSHA256(file, chunkSize, function (percent) {
            // 节流：每 2% 上报一次（4MB/块，10GB 文件将产生 2560 条消息），
            // 高频 postMessage 会挤占主线程，导致大文件 hash 阶段 UI 卡顿。
            if (percent - lastReport >= 2 || percent >= 100) {
                lastReport = percent;
                self.postMessage({ type: 'progress', percent: percent });
            }
        });
        self.postMessage({ type: 'done', hash: hash });
    } catch (err) {
        self.postMessage({ type: 'error', error: String(err && err.message || err) });
    }
};
