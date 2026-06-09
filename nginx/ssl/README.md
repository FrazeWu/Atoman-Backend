# SSL Configuration for Cloudflare

This directory is for SSL certificates if you want to use Cloudflare Origin Certificates for end-to-end encryption.

## Cloudflare Setup Options

### Option 1: Flexible SSL (Recommended for Quick Setup)
- Cloudflare handles SSL
- No certificates needed in Nginx
- Traffic between Cloudflare and origin is HTTP
- **Current configuration supports this**

### Option 2: Full SSL (Cloudflare Origin Certificate)
- End-to-end encryption
- Requires Cloudflare Origin Certificate

#### Steps:
1. Go to Cloudflare Dashboard → SSL/TLS → Origin Server
2. Create Origin Certificate
3. Copy certificate and private key from Cloudflare
4. Save files here:
   - `nginx/ssl/cert.pem` - Certificate (paste from Cloudflare)
   - `nginx/ssl/key.pem` - Private key (paste from Cloudflare)
   
   **Note:** `.pem` and `.crt` formats are identical. Cloudflare provides PEM format which works perfectly with Nginx.
   
5. Update nginx configuration:
   ```bash
   cp nginx/nginx-ssl.conf web/nginx.conf
   ```
6. Rebuild containers:
   ```bash
   docker-compose down
   docker-compose up --build -d
   ```

### Option 3: Full SSL (Strict)
- Use your own SSL certificate (Let's Encrypt, etc.)
- Place certificate files here:
  - `nginx/ssl/fullchain.pem`
  - `nginx/ssl/privkey.pem`

## Security Note
- Never commit SSL certificates to git
- Files in this directory are ignored by `.gitignore`
