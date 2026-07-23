#ifndef IMAGEFILECONVERTER_H
#define IMAGEFILECONVERTER_H

#include <QString>
#include "image_capabilities.h"

// Simple base class to function as the parent to the mainimageconverter
// Main purpose was to allow for future support of new image formats
class ImageFileConverter
{
    public:
        virtual ~ImageFileConverter() {}
        bool convert_image(const QString &input_path, const QString &output_path, const ImageFormatCapabilities &settings, QString &error_message);
};

#endif //IMAGEFILECONVERTER
