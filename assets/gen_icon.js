// 生成 LimeLink 青柠图标：PNG（多尺寸）+ ICO（托盘用）
// 纯 Node 实现，不依赖外部图片库（手写 PNG 编码 + ICO 打包）
// 用法: node gen_icon.js
const zlib = require('zlib');
const fs = require('fs');
const path = require('path');

// ---- PNG 编码 ----
function crc32(buf) {
  let c, table = crc32.table;
  if (!table) {
    table = crc32.table = [];
    for (let n = 0; n < 256; n++) {
      c = n;
      for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
      table[n] = c >>> 0;
    }
  }
  let crc = 0xffffffff;
  for (let i = 0; i < buf.length; i++) crc = table[(crc ^ buf[i]) & 0xff] ^ (crc >>> 8);
  return (crc ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length, 0);
  const typeBuf = Buffer.from(type, 'ascii');
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(Buffer.concat([typeBuf, data])), 0);
  return Buffer.concat([len, typeBuf, data, crc]);
}

function encodePNG(width, height, rgba) {
  const sig = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]);
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(width, 0);
  ihdr.writeUInt32BE(height, 4);
  ihdr[8] = 8;   // bit depth
  ihdr[9] = 6;   // color type RGBA
  ihdr[10] = 0; ihdr[11] = 0; ihdr[12] = 0;
  // 原始像素 + filter byte 每行
  const raw = Buffer.alloc((width * 4 + 1) * height);
  let p = 0;
  for (let y = 0; y < height; y++) {
    raw[p++] = 0; // filter none
    for (let x = 0; x < width; x++) {
      const i = (y * width + x) * 4;
      raw[p++] = rgba[i];
      raw[p++] = rgba[i + 1];
      raw[p++] = rgba[i + 2];
      raw[p++] = rgba[i + 3];
    }
  }
  const idat = zlib.deflateSync(raw, { level: 9 });
  return Buffer.concat([
    sig,
    chunk('IHDR', ihdr),
    chunk('IDAT', idat),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

// ---- 绘制青柠图标 ----
function drawLime(size) {
  const rgba = Buffer.alloc(size * size * 4);
  const cx = size / 2, cy = size / 2;
  const bgR = size * 0.46;   // 背景圆半径
  const pulpR = size * 0.32; // 果肉半径
  const bg = [0x7c, 0xb3, 0x42];   // 青柠绿
  const bgEdge = [0x55, 0x8b, 0x2f];
  const pulp = [0xae, 0xd5, 0x81]; // 浅绿果肉
  const line = [0x9c, 0xcc, 0x65]; // 纹理

  function set(x, y, color, alpha) {
    if (x < 0 || y < 0 || x >= size || y >= size) return;
    const i = (y * size + x) * 4;
    rgba[i] = color[0];
    rgba[i + 1] = color[1];
    rgba[i + 2] = color[2];
    rgba[i + 3] = alpha;
  }

  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const dx = x - cx, dy = y - cy;
      const d = Math.sqrt(dx * dx + dy * dy);
      if (d <= bgR) {
        // 边缘描边
        const isEdge = d > bgR - size * 0.015;
        set(x, y, isEdge ? bgEdge : bg, 255);
      }
    }
  }

  // 果肉
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const dx = x - cx, dy = y - cy;
      const d = Math.sqrt(dx * dx + dy * dy);
      if (d <= pulpR) set(x, y, pulp, 255);
    }
  }

  // 纹理十字线
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const dx = Math.abs(x - cx), dy = Math.abs(y - cy);
      const d = Math.sqrt((x - cx) ** 2 + (y - cy) ** 2);
      if (d <= pulpR) {
        const onLine = dx < size * 0.02 || dy < size * 0.02 ||
          Math.abs(Math.abs(x - cx) - Math.abs(y - cy)) < size * 0.02;
        if (onLine && !(dx < size * 0.02 && dy < size * 0.02)) set(x, y, line, 255);
      }
    }
  }

  // 叶子（右上）
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const ex = x - cx, ey = y - cy;
      // 叶子大致在右上角，离中心 bgR 到 bgR*1.1 的区域
      const d = Math.sqrt(ex * ex + ey * ey);
      if (d > bgR * 0.8 && d < bgR * 1.1 && ex > 0 && ey < 0) {
        set(x, y, bgEdge, 255);
      }
    }
  }

  // 高光
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      const dx = x - cx * 0.75, dy = y - cy * 0.75;
      const d = Math.sqrt(dx * dx + dy * dy);
      if (d < pulpR * 0.45 && x < cx && y < cy) set(x, y, [0xff, 0xff, 0xff], 60);
    }
  }

  return rgba;
}

// ---- ICO 打包（内嵌 PNG，多尺寸）----
// images: [{size, png}]，按尺寸升序打包成单个 ICO 容器
function encodeICO(images) {
  const header = Buffer.alloc(6);
  header.writeUInt16LE(0, 0);          // reserved
  header.writeUInt16LE(1, 2);          // type icon
  header.writeUInt16LE(images.length, 4); // count
  const dataStart = 6 + 16 * images.length;
  const entries = [];
  let offset = dataStart;
  for (const { size, png } of images) {
    const entry = Buffer.alloc(16);
    entry[0] = size >= 256 ? 0 : size; // width（256 记为 0）
    entry[1] = size >= 256 ? 0 : size; // height
    entry[2] = 0;                      // palette
    entry[3] = 0;                      // reserved
    entry.writeUInt16LE(1, 4);           // planes
    entry.writeUInt16LE(32, 6);          // bit count
    entry.writeUInt32LE(png.length, 8);  // 数据长度
    entry.writeUInt32LE(offset, 12);     // 数据偏移
    entries.push(entry);
    offset += png.length;
  }
  return Buffer.concat([header, ...entries, ...images.map(i => i.png)]);
}

const outDir = path.join(__dirname, 'icon');
fs.mkdirSync(outDir, { recursive: true });

// 生成尺寸（24 为任务栏缩放常用；256 供资源管理器大图标）
const sizes = [16, 24, 32, 48, 64, 128, 256];
for (const s of sizes) {
  const png = encodePNG(s, s, drawLime(s));
  fs.writeFileSync(path.join(outDir, `lime_${s}.png`), png);
  console.log(`lime_${s}.png (${png.length} bytes)`);
}

// ICO：打包全部尺寸，托盘与 exe 嵌入共用
const icoImages = sizes.map(s => ({ size: s, png: encodePNG(s, s, drawLime(s)) }));
const ico = encodeICO(icoImages);
fs.writeFileSync(path.join(outDir, 'lime.ico'), ico);
console.log(`lime.ico (${ico.length} bytes, ${sizes.join('/')} px)`);

// v1.1.0 说明：生成后需手动把图标复制到两处 embed 目录：
//   assets/icon/lime.ico        → pc/cmd/limehost/assets/lime.ico（托盘）
//   assets/icon/lime_32.png     → pc/cmd/limehost/assets/icons/{wechat,sms,system}.png（备用）
//   assets/icon/lime_32.png     → pc/internal/notifier/assets/{wechat,sms,system}.png（Toast 小图标）
// pc/ 根目录下的 lime.ico、icons/ 已清理，不再自动生成。