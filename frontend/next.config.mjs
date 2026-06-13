/** @type {import('next').NextConfig} */
const nextConfig = {
  reactStrictMode: true,
  images: {
    // Allow Spotify album art and S3 asset hosts.
    remotePatterns: [
      { protocol: 'https', hostname: 'i.scdn.co' },
      { protocol: 'https', hostname: '*.amazonaws.com' },
    ],
  },
};

export default nextConfig;
