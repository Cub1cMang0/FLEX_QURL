#ifndef IMAGE_CAPABILITIES_H
#define IMAGE_CAPABILITIES_H

#include <QMap>
#include <QString>

// Use struct to map out what capabilities are possible
struct ImageFormatCapabilities
{
    bool quality_support = false;
    bool alpha_support = false;
    bool grayscale_support = false;
    bool bit_depth_support = false;
};

// inline const to map out each image format and their capabilities 
// for better searching / indexing
inline const QMap<QString, ImageFormatCapabilities> image_capabilities =
    {
        {"png",  {true, true, true, true}},
        {"jpg",  {true, false, true, false}},
        {"jpeg", {true, false, true, false}},
        {"ico",  {false, true, true, true}},
        {"jfif", {true, false, true, false}},
        {"pbm",  {false, false, false, false}},
        {"pgm",  {false, false, true, false}},
        {"ppm",  {false, false, false, false}},
        {"bmp",  {false, false, true, true}},
        {"cur",  {false, true, true, true}},
        {"xbm",  {false, false, false, false}},
        {"xpm",  {false, true, true, false}}
};

#endif // IMAGE_CAPABILITIES_H
