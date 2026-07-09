// crypto.js — 前端 RSA 公钥加密封装（用于加密 password 字段传输）
// 依赖：jsencrypt.min.js（需在本文件之前加载）
// 设计：从 /api/pubkey 获取 RSA 公钥（PKIX PEM 格式），用 jsencrypt 加密明文，返回 base64 密文
// 安全策略：加密失败时 reject（不回退明文），后端 DecryptPassword 解密失败也拒绝请求
(function () {
    'use strict';

    // 缓存公钥，避免每次加密都请求 /api/pubkey
    let cachedPublicKey = null;
    let publicKeyPromise = null;

    /**
     * fetchPublicKey - 从 /api/pubkey 获取 RSA 公钥（带缓存）
     * 并发场景下复用同一个 Promise，避免重复请求
     * @returns {Promise<string>} PEM 格式公钥
     */
    function fetchPublicKey() {
        if (cachedPublicKey) return Promise.resolve(cachedPublicKey);
        if (publicKeyPromise) return publicKeyPromise;
        publicKeyPromise = fetch('/api/pubkey', { credentials: 'same-origin' })
            .then(function (resp) {
                if (!resp.ok) throw new Error('fetch pubkey failed: ' + resp.status);
                return resp.json();
            })
            .then(function (data) {
                if (!data || !data.public_key) throw new Error('pubkey response missing public_key');
                cachedPublicKey = data.public_key;
                return cachedPublicKey;
            })
            .catch(function (err) {
                publicKeyPromise = null;
                throw err;
            });
        return publicKeyPromise;
    }

    /**
     * encryptPassword - 用 RSA 公钥加密 password 明文
     * @param {string} plaintext - 密码明文
     * @returns {Promise<string>} base64 编码的密文；加密失败时 reject（不回退明文）
     */
    window.encryptPassword = function (plaintext) {
        if (!plaintext) return Promise.resolve('');
        if (typeof window.JSEncrypt === 'undefined') {
            return Promise.reject(new Error('JSEncrypt 未加载，无法加密密码'));
        }
        return fetchPublicKey().then(function (pubKey) {
            const encrypt = new window.JSEncrypt();
            encrypt.setPublicKey(pubKey);
            const encrypted = encrypt.encrypt(plaintext);
            if (!encrypted) throw new Error('JSEncrypt.encrypt 返回空值');
            return encrypted;
        });
    };
})();
